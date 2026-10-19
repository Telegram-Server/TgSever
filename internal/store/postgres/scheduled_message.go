package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

const maxScheduledMessagePage = 100

// scheduledRetryBackoffSeconds 是后台派发投递失败后的最小重试间隔：投递失败的
// pending 行（last_error 非空）在该退避窗口内不再被 ClaimDueScheduledMessages
// 认领，避免对端注销等永久失败的到期消息每秒被反复 claim+send。
const scheduledRetryBackoffSeconds = 30

// 定时消息的 SQL 全部在 queries/scheduled_message.sql（sqlc）；读出统一返回整行
// sqlcgen.ScheduledMessage，经 scheduledMessageFromRow 解码。

func (s *MessageStore) CreateScheduledMessage(ctx context.Context, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
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
	entities, err := encodeMessageEntities(req.Entities)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	media, err := encodeMessageMedia(req.Media)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	richMessage, err := encodeRichMessage(req.RichMessage)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	meta, err := messageMetadataParamsFrom(req.Silent, req.NoForwards, req.ReplyTo, req.Forward)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("begin create scheduled message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := sqlcgen.New(tx)
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("lock scheduled owner: %w", err)
	}
	if req.RandomID != 0 {
		existing, ok, err := getPendingScheduledByRandomID(ctx, q, req.OwnerUserID, req.RandomID)
		if err != nil {
			return domain.ScheduledMessage{}, err
		}
		if ok {
			if err := tx.Commit(ctx); err != nil {
				return domain.ScheduledMessage{}, fmt.Errorf("commit duplicate scheduled message: %w", err)
			}
			committed = true
			return existing, nil
		}
	}
	nextID, err := nextScheduledMessageID(ctx, q, req.OwnerUserID)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	sendAsType, sendAsID := "", int64(0)
	if req.SendAs != nil {
		sendAsType = string(req.SendAs.Type)
		sendAsID = req.SendAs.ID
	}
	fwdChannelPost := int32(0)
	if req.Forward != nil {
		fwdChannelPost = int32(req.Forward.ChannelPost)
	}
	row, err := q.InsertScheduledMessage(ctx, sqlcgen.InsertScheduledMessageParams{
		OwnerUserID:          req.OwnerUserID,
		ScheduledID:          int32(nextID),
		PeerType:             string(req.Peer.Type),
		PeerID:               req.Peer.ID,
		RandomID:             req.RandomID,
		MessageDate:          int32(req.Date),
		Body:                 req.Message,
		Entities:             entities,
		Media:                media,
		RichMessage:          richMessage,
		Silent:               req.Silent,
		Noforwards:           req.NoForwards,
		ReplyToMsgID:         meta.ReplyToMsgID,
		ReplyToPeerType:      meta.ReplyToPeerType,
		ReplyToPeerID:        meta.ReplyToPeerID,
		ReplyToTopID:         meta.ReplyToTopID,
		QuoteText:            meta.QuoteText,
		QuoteEntities:        meta.QuoteEntitiesJSON,
		QuoteOffset:          meta.QuoteOffset,
		FwdFromPeerType:      meta.FwdFromPeerType,
		FwdFromPeerID:        meta.FwdFromPeerID,
		FwdFromName:          meta.FwdFromName,
		FwdDate:              meta.FwdDate,
		FwdChannelPost:       fwdChannelPost,
		FwdSavedFromPeerType: meta.FwdSavedFromPeerType,
		FwdSavedFromPeerID:   meta.FwdSavedFromPeerID,
		FwdSavedFromMsgID:    meta.FwdSavedFromMsgID,
		SendAsPeerType:       sendAsType,
		SendAsPeerID:         sendAsID,
		GroupedID:            req.GroupedID,
		ScheduleDate:         int32(req.ScheduleDate),
		ScheduleRepeatPeriod: int32(req.ScheduleRepeatPeriod),
	})
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("insert scheduled message: %w", err)
	}
	msg, err := scheduledMessageFromRow(row)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := setHasScheduledTx(ctx, q, req.OwnerUserID, req.Peer, true); err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("commit create scheduled message: %w", err)
	}
	committed = true
	return msg, nil
}

func (s *MessageStore) ListScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return listScheduledMessages(ctx, s.q, filter, false)
}

func (s *MessageStore) EditScheduledMessage(ctx context.Context, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	if req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ID <= 0 || req.ScheduleDate <= 0 {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ScheduledMessage{}, fmt.Errorf("edit scheduled message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("begin edit scheduled message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := sqlcgen.New(tx)
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("lock scheduled edit owner: %w", err)
	}
	current, ok, err := getPendingScheduledForEdit(ctx, q, req.OwnerUserID, req.Peer, req.ID)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if !ok {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	message := current.Message
	entities := append([]domain.MessageEntity(nil), current.Entities...)
	richMessage := current.RichMessage
	if req.SetMessage {
		message = req.Message
		entities = append([]domain.MessageEntity(nil), req.Entities...)
	}
	if req.SetRichMessage {
		richMessage = req.RichMessage
	}
	if message == "" && current.Media.IsZero() && richMessage.IsZero() {
		return domain.ScheduledMessage{}, domain.ErrMessageEmpty
	}
	encodedEntities, err := encodeMessageEntities(entities)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	encodedRichMessage, err := encodeRichMessage(richMessage)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	row, err := q.UpdateScheduledMessageContent(ctx, sqlcgen.UpdateScheduledMessageContentParams{
		Body:         message,
		Entities:     encodedEntities,
		RichMessage:  encodedRichMessage,
		ScheduleDate: int32(req.ScheduleDate),
		UpdatedAt:    int32(req.Date),
		OwnerUserID:  req.OwnerUserID,
		PeerType:     string(req.Peer.Type),
		PeerID:       req.Peer.ID,
		ScheduledID:  int32(req.ID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
		}
		return domain.ScheduledMessage{}, fmt.Errorf("edit scheduled message: %w", err)
	}
	msg, err := scheduledMessageFromRow(row)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("commit edit scheduled message: %w", err)
	}
	committed = true
	return msg, nil
}

func (s *MessageStore) GetScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return listScheduledMessages(ctx, s.q, filter, true)
}

func (s *MessageStore) DeleteScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter, date int) ([]domain.ScheduledMessage, error) {
	if filter.OwnerUserID == 0 || filter.Peer.ID == 0 || len(filter.IDs) == 0 {
		return nil, nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("delete scheduled messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin delete scheduled messages: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := sqlcgen.New(tx)
	if err := lockUsersForUpdate(ctx, tx, filter.OwnerUserID); err != nil {
		return nil, fmt.Errorf("lock scheduled delete owner: %w", err)
	}
	rows, err := q.MarkScheduledMessagesDeleted(ctx, sqlcgen.MarkScheduledMessagesDeletedParams{
		UpdatedAt:   int32(date),
		OwnerUserID: filter.OwnerUserID,
		PeerType:    string(filter.Peer.Type),
		PeerID:      filter.Peer.ID,
		Ids:         intSliceToInt32(filter.IDs),
	})
	if err != nil {
		return nil, fmt.Errorf("delete scheduled messages: %w", err)
	}
	deleted, err := scheduledMessagesFromRows(rows)
	if err != nil {
		return nil, err
	}
	if err := refreshHasScheduledTx(ctx, q, filter.OwnerUserID, filter.Peer); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delete scheduled messages: %w", err)
	}
	committed = true
	return deleted, nil
}

func (s *MessageStore) ClaimScheduledMessages(ctx context.Context, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if claim.OwnerUserID == 0 || claim.Peer.ID == 0 || len(claim.IDs) == 0 {
		return nil, nil
	}
	if claim.Now == 0 {
		claim.Now = int(time.Now().Unix())
	}
	if claim.LeaseUntil == 0 {
		claim.LeaseUntil = claim.Now + 60
	}
	rows, err := s.q.ClaimScheduledMessagesByIDs(ctx, sqlcgen.ClaimScheduledMessagesByIDsParams{
		LeaseUntil:  int32(claim.LeaseUntil),
		NowUnix:     int32(claim.Now),
		OwnerUserID: claim.OwnerUserID,
		PeerType:    string(claim.Peer.Type),
		PeerID:      claim.Peer.ID,
		Ids:         intSliceToInt32(claim.IDs),
	})
	if err != nil {
		return nil, fmt.Errorf("claim scheduled messages: %w", err)
	}
	return scheduledMessagesFromRows(rows)
}

func (s *MessageStore) ClaimDueScheduledMessages(ctx context.Context, now, limit, leaseSeconds int) ([]domain.ScheduledMessage, error) {
	if now <= 0 || limit <= 0 {
		return nil, nil
	}
	if limit > maxScheduledMessagePage {
		limit = maxScheduledMessagePage
	}
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	// retryFloor 让上一次投递失败（last_error 非空）的 pending 行至少退避
	// scheduledRetryBackoffSeconds 秒再被认领；同时回收 lease 已过期却仍卡在
	// 'dispatching' 的行——派发器在 claim 之后、MarkScheduledMessageSent 之前
	// 崩溃/部署/OOM 会留下这种行，旧实现只认领 'pending' 导致该定时消息永久不
	// 投递且 has_scheduled 永远为真。重投由 random_id 幂等兜底（命中 Duplicate
	// 仍会 MarkScheduledMessageSent 清状态）。
	retryFloor := now - scheduledRetryBackoffSeconds
	rows, err := s.q.ClaimDueScheduledMessages(ctx, sqlcgen.ClaimDueScheduledMessagesParams{
		NowUnix:      int32(now),
		LeaseSeconds: int32(leaseSeconds),
		RetryFloor:   int32(retryFloor),
		LimitCount:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim due scheduled messages: %w", err)
	}
	return scheduledMessagesFromRows(rows)
}

func (s *MessageStore) MarkScheduledMessageSent(ctx context.Context, ownerUserID int64, id, sentMessageID, date int) error {
	if ownerUserID == 0 || id <= 0 {
		return nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fmt.Errorf("mark scheduled sent: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin mark scheduled sent: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := sqlcgen.New(tx)
	if err := lockUsersForUpdate(ctx, tx, ownerUserID); err != nil {
		return fmt.Errorf("lock scheduled sent owner: %w", err)
	}
	sent, err := q.MarkScheduledMessageSent(ctx, sqlcgen.MarkScheduledMessageSentParams{
		SentMessageID: int32(sentMessageID),
		UpdatedAt:     int32(date),
		OwnerUserID:   ownerUserID,
		ScheduledID:   int32(id),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("mark scheduled sent: %w", err)
	}
	if err := refreshHasScheduledTx(ctx, q, ownerUserID, domain.Peer{Type: domain.PeerType(sent.PeerType), ID: sent.PeerID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mark scheduled sent: %w", err)
	}
	committed = true
	return nil
}

func (s *MessageStore) ReleaseScheduledMessage(ctx context.Context, ownerUserID int64, id int, errText string) error {
	if ownerUserID == 0 || id <= 0 {
		return nil
	}
	if err := s.q.ReleaseScheduledMessage(ctx, sqlcgen.ReleaseScheduledMessageParams{
		LastError:   errText,
		OwnerUserID: ownerUserID,
		ScheduledID: int32(id),
	}); err != nil {
		return fmt.Errorf("release scheduled message: %w", err)
	}
	return nil
}

func (s *MessageStore) HasScheduledMessages(ctx context.Context, ownerUserID int64, peer domain.Peer) (bool, error) {
	if ownerUserID == 0 || peer.ID == 0 {
		return false, nil
	}
	exists, err := s.q.HasActiveScheduledMessages(ctx, sqlcgen.HasActiveScheduledMessagesParams{
		OwnerUserID: ownerUserID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
	})
	if err != nil {
		return false, fmt.Errorf("has scheduled messages: %w", err)
	}
	return exists, nil
}

func listScheduledMessages(ctx context.Context, q *sqlcgen.Queries, filter domain.ScheduledMessageFilter, exact bool) (domain.ScheduledMessageList, error) {
	if filter.OwnerUserID == 0 || filter.Peer.ID == 0 {
		return domain.ScheduledMessageList{}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > maxScheduledMessagePage {
		limit = maxScheduledMessagePage
	}
	var (
		rows []sqlcgen.ScheduledMessage
		err  error
	)
	if exact {
		if len(filter.IDs) == 0 {
			return domain.ScheduledMessageList{}, nil
		}
		rows, err = q.ListScheduledMessagesByIDs(ctx, sqlcgen.ListScheduledMessagesByIDsParams{
			OwnerUserID: filter.OwnerUserID,
			PeerType:    string(filter.Peer.Type),
			PeerID:      filter.Peer.ID,
			Ids:         intSliceToInt32(filter.IDs),
			LimitCount:  int32(limit),
		})
	} else {
		rows, err = q.ListScheduledMessagesByPeer(ctx, sqlcgen.ListScheduledMessagesByPeerParams{
			OwnerUserID: filter.OwnerUserID,
			PeerType:    string(filter.Peer.Type),
			PeerID:      filter.Peer.ID,
			LimitCount:  int32(limit),
		})
	}
	if err != nil {
		return domain.ScheduledMessageList{}, fmt.Errorf("list scheduled messages: %w", err)
	}
	msgs, err := scheduledMessagesFromRows(rows)
	if err != nil {
		return domain.ScheduledMessageList{}, err
	}
	out := domain.ScheduledMessageList{Messages: msgs, Count: len(msgs), Hash: scheduledMessageListHash(msgs)}
	if filter.Hash != 0 && filter.Hash == out.Hash {
		out.Messages = nil
		out.Count = 0
	}
	return out, nil
}

func scheduledMessagesFromRows(rows []sqlcgen.ScheduledMessage) ([]domain.ScheduledMessage, error) {
	out := make([]domain.ScheduledMessage, 0, len(rows))
	for _, row := range rows {
		msg, err := scheduledMessageFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func scheduledMessageFromRow(row sqlcgen.ScheduledMessage) (domain.ScheduledMessage, error) {
	msg := domain.ScheduledMessage{
		OwnerUserID:          row.OwnerUserID,
		ID:                   int(row.ScheduledID),
		Peer:                 domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		RandomID:             row.RandomID,
		Message:              row.Body,
		Silent:               row.Silent,
		NoForwards:           row.Noforwards,
		GroupedID:            row.GroupedID,
		ScheduleDate:         int(row.ScheduleDate),
		ScheduleRepeatPeriod: int(row.ScheduleRepeatPeriod),
		CreatedAt:            int(row.CreatedAt),
		UpdatedAt:            int(row.UpdatedAt),
		State:                row.State,
		SentMessageID:        int(row.SentMessageID),
	}
	entities, err := decodeMessageEntities(string(row.Entities))
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled entities: %w", err)
	}
	msg.Entities = entities
	media, err := decodeMessageMedia(string(row.Media))
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled media: %w", err)
	}
	msg.Media = media
	richMessage, err := decodeRichMessage(string(row.RichMessage))
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled rich message: %w", err)
	}
	msg.RichMessage = richMessage
	// 转发头完整落表（含 channel_post 与转发进收藏夹的 saved_from），到点发出原样还原。
	_, _, reply, forward, err := messageMetadataFromFields(
		msg.Silent, msg.NoForwards, row.ReplyToMsgID, row.ReplyToPeerType, row.ReplyToPeerID, row.ReplyToTopID,
		0, // scheduled_messages 不持久化 story 回复（定时回复 story 是边角，恒 0）
		row.QuoteText, string(row.QuoteEntities), row.QuoteOffset, "{}",
		row.FwdFromPeerType, row.FwdFromPeerID, row.FwdFromName, row.FwdDate,
		row.FwdSavedFromPeerType, row.FwdSavedFromPeerID, row.FwdSavedFromMsgID,
	)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled metadata: %w", err)
	}
	if forward != nil {
		forward.ChannelPost = int(row.FwdChannelPost)
	}
	if reply == nil && row.ReplyToTopID > 0 {
		// 只指向 forum 话题、不回复具体消息的定时消息（如定时转发进话题）：reply_to_msg_id 为 0，
		// 话题落在 reply_to_top_id。共享解码只认 reply_to_msg_id，这里补回，否则发出时落进 General。
		reply = &domain.MessageReply{
			Peer:         domain.Peer{Type: domain.PeerType(row.ReplyToPeerType), ID: row.ReplyToPeerID},
			TopMessageID: int(row.ReplyToTopID),
			ForumTopic:   true,
		}
	}
	msg.ReplyTo = reply
	msg.Forward = forward
	if row.SendAsPeerType != "" && row.SendAsPeerID != 0 {
		msg.SendAs = &domain.Peer{Type: domain.PeerType(row.SendAsPeerType), ID: row.SendAsPeerID}
	}
	return msg, nil
}

func getPendingScheduledByRandomID(ctx context.Context, q *sqlcgen.Queries, ownerUserID, randomID int64) (domain.ScheduledMessage, bool, error) {
	row, err := q.GetPendingScheduledMessageByRandomID(ctx, sqlcgen.GetPendingScheduledMessageByRandomIDParams{
		OwnerUserID: ownerUserID,
		RandomID:    randomID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ScheduledMessage{}, false, nil
	}
	if err != nil {
		return domain.ScheduledMessage{}, false, fmt.Errorf("get scheduled by random_id: %w", err)
	}
	msg, err := scheduledMessageFromRow(row)
	if err != nil {
		return domain.ScheduledMessage{}, false, err
	}
	return msg, true, nil
}

func getPendingScheduledForEdit(ctx context.Context, q *sqlcgen.Queries, ownerUserID int64, peer domain.Peer, id int) (domain.ScheduledMessage, bool, error) {
	row, err := q.GetPendingScheduledMessageForEdit(ctx, sqlcgen.GetPendingScheduledMessageForEditParams{
		OwnerUserID: ownerUserID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
		ScheduledID: int32(id),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ScheduledMessage{}, false, nil
	}
	if err != nil {
		return domain.ScheduledMessage{}, false, fmt.Errorf("get scheduled for edit: %w", err)
	}
	msg, err := scheduledMessageFromRow(row)
	if err != nil {
		return domain.ScheduledMessage{}, false, err
	}
	return msg, true, nil
}

func nextScheduledMessageID(ctx context.Context, q *sqlcgen.Queries, ownerUserID int64) (int, error) {
	next, err := q.NextScheduledMessageID(ctx, ownerUserID)
	if err != nil {
		return 0, fmt.Errorf("next scheduled message id: %w", err)
	}
	id := int(next)
	if id <= 0 || id > domain.MaxMessageBoxID {
		return 0, domain.ErrMessageIDInvalid
	}
	return id, nil
}

func setHasScheduledTx(ctx context.Context, q *sqlcgen.Queries, ownerUserID int64, peer domain.Peer, has bool) error {
	switch peer.Type {
	case domain.PeerTypeUser:
		if err := q.UpsertPrivateDialogHasScheduled(ctx, sqlcgen.UpsertPrivateDialogHasScheduledParams{
			UserID:       ownerUserID,
			PeerID:       peer.ID,
			HasScheduled: has,
		}); err != nil {
			return fmt.Errorf("set private has_scheduled: %w", err)
		}
		return nil
	case domain.PeerTypeChannel:
		if err := q.SetChannelDialogHasScheduled(ctx, sqlcgen.SetChannelDialogHasScheduledParams{
			HasScheduled: has,
			UserID:       ownerUserID,
			ChannelID:    peer.ID,
		}); err != nil {
			return fmt.Errorf("set channel has_scheduled: %w", err)
		}
		return nil
	default:
		return nil
	}
}

func refreshHasScheduledTx(ctx context.Context, q *sqlcgen.Queries, ownerUserID int64, peer domain.Peer) error {
	exists, err := q.HasActiveScheduledMessages(ctx, sqlcgen.HasActiveScheduledMessagesParams{
		OwnerUserID: ownerUserID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
	})
	if err != nil {
		return fmt.Errorf("refresh scheduled flag: %w", err)
	}
	return setHasScheduledTx(ctx, q, ownerUserID, peer, exists)
}

func intSliceToInt32(ids []int) []int32 {
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id > 0 && id <= domain.MaxMessageBoxID {
			out = append(out, int32(id))
		}
	}
	return out
}

func scheduledMessageListHash(messages []domain.ScheduledMessage) int64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, msg := range messages {
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.ID))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.Peer.ID))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.ScheduleDate))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.UpdatedAt))
		_, _ = h.Write(buf[:])
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
