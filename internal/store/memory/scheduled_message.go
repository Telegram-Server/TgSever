package memory

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// 定时消息的内存实现，语义逐条对齐 postgres（scheduled_message.go +
// queries/scheduled_message.sql）：
//   - scheduled_id 按 owner 取 MAX+1（含已发出/已删除行，id 不复用）；
//   - 同一 owner 仍在队列（pending/dispatching）里的同 random_id 原样返回（排程幂等）；
//   - 列表/获取按 owner+peer，只见 pending/dispatching，按 schedule_date、id 排序；
//   - 状态 pending → dispatching → sent/deleted，发出与删除只改状态不删行；
//   - 到期认领按 schedule_date，跳过租约未到期的 dispatching 与重试退避中的失败行；
//   - 内容只保留 scheduled_messages 有列的字段，读出形态与 postgres 解码结果一致。
//
// 与 postgres 的差异：不维护 dialogs/channel_dialogs.has_scheduled 标志（内存对话
// 由 DialogStore/ChannelStore 各自持有，消息 store 够不到频道对话）；
// HasScheduledMessages 直接按队列判断。

const (
	maxMemoryScheduledPage = 100
	// memoryScheduledRetryBackoffSeconds 与 postgres 的 scheduledRetryBackoffSeconds 相同。
	memoryScheduledRetryBackoffSeconds = 30
)

type scheduledMessageQueue struct {
	mu   sync.Mutex
	rows []*scheduledMessageRow
}

// scheduledMessageRow 是一行 scheduled_messages：domain 字段加上不出 store 的
// lease_until 与 last_error 两列。
type scheduledMessageRow struct {
	msg        domain.ScheduledMessage
	leaseUntil int
	lastError  string
}

func (r *scheduledMessageRow) active() bool {
	return r.msg.State == "pending" || r.msg.State == "dispatching"
}

func (s *MessageStore) CreateScheduledMessage(_ context.Context, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	if req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ScheduleDate <= 0 {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: invalid request")
	}
	if req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: invalid peer")
	}
	if req.Message == "" && req.Media.IsZero() && req.RichMessage.IsZero() {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: empty message")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return domain.ScheduledMessage{}, err
	}
	if req.Forward != nil && req.Forward.Date < 0 {
		return domain.ScheduledMessage{}, fmt.Errorf("forward metadata: invalid date")
	}
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	if req.RandomID != 0 {
		var existing *scheduledMessageRow
		for _, row := range q.rows {
			if row.msg.OwnerUserID == req.OwnerUserID && row.msg.RandomID == req.RandomID && row.active() &&
				(existing == nil || row.msg.ID < existing.msg.ID) {
				existing = row
			}
		}
		if existing != nil {
			return cloneScheduledMessage(existing.msg), nil
		}
	}
	nextID := 1
	for _, row := range q.rows {
		if row.msg.OwnerUserID == req.OwnerUserID && row.msg.ID >= nextID {
			nextID = row.msg.ID + 1
		}
	}
	if nextID > domain.MaxMessageBoxID {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	msg := domain.ScheduledMessage{
		OwnerUserID:          req.OwnerUserID,
		ID:                   nextID,
		Peer:                 req.Peer,
		RandomID:             req.RandomID,
		Message:              req.Message,
		Entities:             storedScheduledEntities(req.Entities),
		Media:                storedScheduledMedia(req.Media),
		RichMessage:          storedScheduledRichMessage(req.RichMessage),
		Silent:               req.Silent,
		NoForwards:           req.NoForwards,
		ReplyTo:              storedScheduledReply(req.ReplyTo),
		Forward:              storedScheduledForward(req.Forward),
		SendAs:               storedScheduledSendAs(req.SendAs),
		GroupedID:            req.GroupedID,
		ScheduleDate:         req.ScheduleDate,
		ScheduleRepeatPeriod: req.ScheduleRepeatPeriod,
		CreatedAt:            req.Date,
		UpdatedAt:            req.Date,
		State:                "pending",
	}
	q.rows = append(q.rows, &scheduledMessageRow{msg: msg})
	return cloneScheduledMessage(msg), nil
}

func (s *MessageStore) EditScheduledMessage(_ context.Context, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	if req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ID <= 0 || req.ScheduleDate <= 0 {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.msg.OwnerUserID != req.OwnerUserID || row.msg.Peer != req.Peer || row.msg.ID != req.ID || row.msg.State != "pending" {
			continue
		}
		message, entities, rich := row.msg.Message, row.msg.Entities, row.msg.RichMessage
		if req.SetMessage {
			message, entities = req.Message, storedScheduledEntities(req.Entities)
		}
		if req.SetRichMessage {
			rich = storedScheduledRichMessage(req.RichMessage)
		}
		if message == "" && row.msg.Media.IsZero() && rich.IsZero() {
			return domain.ScheduledMessage{}, domain.ErrMessageEmpty
		}
		row.msg.Message, row.msg.Entities, row.msg.RichMessage = message, entities, rich
		row.msg.ScheduleDate = req.ScheduleDate
		row.msg.UpdatedAt = req.Date
		return cloneScheduledMessage(row.msg), nil
	}
	return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
}

func (s *MessageStore) ListScheduledMessages(_ context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return s.scheduled.list(filter, false), nil
}

func (s *MessageStore) GetScheduledMessages(_ context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return s.scheduled.list(filter, true), nil
}

func (q *scheduledMessageQueue) list(filter domain.ScheduledMessageFilter, exact bool) domain.ScheduledMessageList {
	if filter.OwnerUserID == 0 || filter.Peer.ID == 0 {
		return domain.ScheduledMessageList{}
	}
	limit := filter.Limit
	if limit <= 0 || limit > maxMemoryScheduledPage {
		limit = maxMemoryScheduledPage
	}
	var ids map[int]bool
	if exact {
		ids = scheduledIDSet(filter.IDs)
		if len(filter.IDs) == 0 {
			return domain.ScheduledMessageList{}
		}
	}
	q.mu.Lock()
	matched := make([]domain.ScheduledMessage, 0)
	for _, row := range q.rows {
		if row.msg.OwnerUserID != filter.OwnerUserID || row.msg.Peer != filter.Peer || !row.active() {
			continue
		}
		if exact && !ids[row.msg.ID] {
			continue
		}
		matched = append(matched, cloneScheduledMessage(row.msg))
	}
	q.mu.Unlock()
	sortScheduledMessages(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := domain.ScheduledMessageList{Messages: matched, Count: len(matched), Hash: memoryScheduledListHash(matched)}
	if filter.Hash != 0 && filter.Hash == out.Hash {
		out.Messages = nil
		out.Count = 0
	}
	return out
}

func (s *MessageStore) DeleteScheduledMessages(_ context.Context, filter domain.ScheduledMessageFilter, date int) ([]domain.ScheduledMessage, error) {
	if filter.OwnerUserID == 0 || filter.Peer.ID == 0 || len(filter.IDs) == 0 {
		return nil, nil
	}
	ids := scheduledIDSet(filter.IDs)
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]domain.ScheduledMessage, 0, len(ids))
	for _, row := range q.rows {
		if row.msg.OwnerUserID != filter.OwnerUserID || row.msg.Peer != filter.Peer || !ids[row.msg.ID] || !row.active() {
			continue
		}
		row.msg.State = "deleted"
		row.msg.UpdatedAt = date
		out = append(out, cloneScheduledMessage(row.msg))
	}
	return out, nil
}

func (s *MessageStore) ClaimScheduledMessages(_ context.Context, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if claim.OwnerUserID == 0 || claim.Peer.ID == 0 || len(claim.IDs) == 0 {
		return nil, nil
	}
	if claim.Now == 0 {
		claim.Now = int(time.Now().Unix())
	}
	if claim.LeaseUntil == 0 {
		claim.LeaseUntil = claim.Now + 60
	}
	ids := scheduledIDSet(claim.IDs)
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]domain.ScheduledMessage, 0, len(ids))
	for _, row := range q.rows {
		if row.msg.OwnerUserID != claim.OwnerUserID || row.msg.Peer != claim.Peer || !ids[row.msg.ID] {
			continue
		}
		if row.msg.State != "pending" && (row.msg.State != "dispatching" || row.leaseUntil > claim.Now) {
			continue
		}
		row.dispatch(claim.Now, claim.LeaseUntil)
		out = append(out, cloneScheduledMessage(row.msg))
	}
	sortScheduledMessages(out)
	return out, nil
}

func (s *MessageStore) ClaimDueScheduledMessages(_ context.Context, now, limit, leaseSeconds int) ([]domain.ScheduledMessage, error) {
	if now <= 0 || limit <= 0 {
		return nil, nil
	}
	if limit > maxMemoryScheduledPage {
		limit = maxMemoryScheduledPage
	}
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	retryFloor := now - memoryScheduledRetryBackoffSeconds
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	due := make([]*scheduledMessageRow, 0)
	for _, row := range q.rows {
		if row.msg.ScheduleDate > now {
			continue
		}
		pendingReady := row.msg.State == "pending" && (row.lastError == "" || row.msg.UpdatedAt <= retryFloor)
		leaseExpired := row.msg.State == "dispatching" && row.leaseUntil <= now
		if pendingReady || leaseExpired {
			due = append(due, row)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		a, b := due[i].msg, due[j].msg
		if a.ScheduleDate != b.ScheduleDate {
			return a.ScheduleDate < b.ScheduleDate
		}
		if a.OwnerUserID != b.OwnerUserID {
			return a.OwnerUserID < b.OwnerUserID
		}
		return a.ID < b.ID
	})
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]domain.ScheduledMessage, 0, len(due))
	for _, row := range due {
		row.dispatch(now, now+leaseSeconds)
		out = append(out, cloneScheduledMessage(row.msg))
	}
	return out, nil
}

func (r *scheduledMessageRow) dispatch(now, leaseUntil int) {
	r.msg.State = "dispatching"
	r.leaseUntil = leaseUntil
	r.msg.UpdatedAt = now
	r.lastError = ""
}

func (s *MessageStore) MarkScheduledMessageSent(_ context.Context, ownerUserID int64, id, sentMessageID, date int) error {
	if ownerUserID == 0 || id <= 0 {
		return nil
	}
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.msg.OwnerUserID == ownerUserID && row.msg.ID == id {
			row.msg.State = "sent"
			row.msg.SentMessageID = sentMessageID
			row.leaseUntil = 0
			row.msg.UpdatedAt = date
		}
	}
	return nil
}

func (s *MessageStore) ReleaseScheduledMessage(_ context.Context, ownerUserID int64, id int, errText string) error {
	if ownerUserID == 0 || id <= 0 {
		return nil
	}
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.msg.OwnerUserID == ownerUserID && row.msg.ID == id && row.msg.State == "dispatching" {
			row.msg.State = "pending"
			row.leaseUntil = 0
			row.lastError = errText
			// 与 postgres 相同取墙钟：重试退避从释放时刻起算。
			row.msg.UpdatedAt = int(time.Now().Unix())
		}
	}
	return nil
}

func (s *MessageStore) HasScheduledMessages(_ context.Context, ownerUserID int64, peer domain.Peer) (bool, error) {
	if ownerUserID == 0 || peer.ID == 0 {
		return false, nil
	}
	q := &s.scheduled
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, row := range q.rows {
		if row.msg.OwnerUserID == ownerUserID && row.msg.Peer == peer && row.active() {
			return true, nil
		}
	}
	return false, nil
}

// scheduledIDSet 与 postgres 的 intSliceToInt32 一样丢掉越界 id。
func scheduledIDSet(ids []int) map[int]bool {
	out := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id > 0 && id <= domain.MaxMessageBoxID {
			out[id] = true
		}
	}
	return out
}

func sortScheduledMessages(rows []domain.ScheduledMessage) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ScheduleDate != rows[j].ScheduleDate {
			return rows[i].ScheduleDate < rows[j].ScheduleDate
		}
		return rows[i].ID < rows[j].ID
	})
}

// memoryScheduledListHash 与 postgres 的 scheduledMessageListHash 逐字节相同。
func memoryScheduledListHash(messages []domain.ScheduledMessage) int64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, msg := range messages {
		for _, v := range []uint64{uint64(msg.ID), uint64(msg.Peer.ID), uint64(msg.ScheduleDate), uint64(msg.UpdatedAt)} {
			binary.LittleEndian.PutUint64(buf[:], v)
			_, _ = h.Write(buf[:])
		}
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// storedScheduledEntities 与 postgres 解码 entities 列一致：恒为非 nil 切片。
func storedScheduledEntities(in []domain.MessageEntity) []domain.MessageEntity {
	out := make([]domain.MessageEntity, len(in))
	copy(out, in)
	return out
}

func storedScheduledMedia(in *domain.MessageMedia) *domain.MessageMedia {
	if in.IsZero() {
		return nil
	}
	return cloneRequestedPeerMedia(in)
}

func storedScheduledRichMessage(in *domain.MessageRichMessage) *domain.MessageRichMessage {
	if in.IsZero() {
		return nil
	}
	return domain.CloneRichMessage(in)
}

// storedScheduledReply 只保留 scheduled_messages 有列的回复字段（reply_to_* 与 quote_*；
// 不存 story 回复与外部回复快照）；只指向 forum 话题的回复按 reply_to_top_id 还原。
func storedScheduledReply(in *domain.MessageReply) *domain.MessageReply {
	if in == nil {
		return nil
	}
	if in.MessageID <= 0 {
		if in.TopMessageID <= 0 {
			return nil
		}
		return &domain.MessageReply{Peer: in.Peer, TopMessageID: in.TopMessageID, ForumTopic: true}
	}
	return &domain.MessageReply{
		MessageID:     in.MessageID,
		Peer:          in.Peer,
		TopMessageID:  in.TopMessageID,
		QuoteText:     in.QuoteText,
		QuoteEntities: storedScheduledEntities(in.QuoteEntities),
		QuoteOffset:   in.QuoteOffset,
	}
}

// storedScheduledForward 按 fwd_* 列还原转发头（含 channel_post 与 saved_from）。
func storedScheduledForward(in *domain.MessageForward) *domain.MessageForward {
	if in == nil || (in.Date == 0 && in.From.ID == 0 && in.FromName == "" && in.SavedFrom.ID == 0) {
		return nil
	}
	out := domain.MessageForward{From: in.From, FromName: in.FromName, Date: in.Date, ChannelPost: in.ChannelPost}
	if in.SavedFrom.ID != 0 {
		out.SavedFrom = in.SavedFrom
		out.SavedFromMsgID = in.SavedFromMsgID
	}
	return &out
}

func storedScheduledSendAs(in *domain.Peer) *domain.Peer {
	if in == nil || in.Type == "" || in.ID == 0 {
		return nil
	}
	out := *in
	return &out
}

// cloneScheduledMessage 让读出的行与队列里的行互不共享可变状态。
func cloneScheduledMessage(msg domain.ScheduledMessage) domain.ScheduledMessage {
	if msg.Entities != nil {
		msg.Entities = storedScheduledEntities(msg.Entities)
	}
	msg.Media = cloneRequestedPeerMedia(msg.Media)
	msg.RichMessage = domain.CloneRichMessage(msg.RichMessage)
	if msg.ReplyTo != nil {
		reply := *msg.ReplyTo
		if reply.QuoteEntities != nil {
			reply.QuoteEntities = storedScheduledEntities(reply.QuoteEntities)
		}
		msg.ReplyTo = &reply
	}
	msg.Forward = cloneMessageForward(msg.Forward)
	if msg.SendAs != nil {
		sendAs := *msg.SendAs
		msg.SendAs = &sendAs
	}
	return msg
}
