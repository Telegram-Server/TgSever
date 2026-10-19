package rpc

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

type captureMessages struct {
	list              domain.MessageList
	filter            domain.MessageFilter
	sendResult        domain.SendPrivateTextResult
	sendUserID        int64
	sendReq           domain.SendPrivateTextRequest
	setThemeUserID    int64
	setThemeReq       domain.SetPrivateChatThemeRequest
	setThemeRes       domain.SetPrivateChatThemeResult
	forwardUserID     int64
	forwardReq        domain.ForwardPrivateMessagesRequest
	forwardRes        domain.ForwardPrivateMessagesResult
	readResult        domain.ReadHistoryResult
	readReq           domain.ReadHistoryRequest
	readPeer          domain.Peer
	readMaxID         int
	readContentsReq   domain.ReadMessageContentsRequest
	readContentsRes   domain.ReadMessageContentsResult
	setReactionReq    domain.SetPrivateMessageReactionsRequest
	setReactionRes    domain.PrivateMessageReactionsResult
	getReactionReq    domain.PrivateMessageReactionsRequest
	getReactionRes    domain.PrivateMessageReactionsResult
	savedTagPeer      domain.Peer
	savedTags         []domain.SavedReactionTag
	updatedSavedTag   domain.SavedReactionTag
	savedTagErr       error
	getMessagesCalls  int
	getMessagesIDs    [][]int
	getMessagesListed bool
	editReq           domain.EditMessageRequest
	editRes           domain.EditMessageResult
	outboxReadDateReq domain.OutboxReadDateRequest
	outboxReadDate    int
	deleteMessagesReq domain.DeleteMessagesRequest
	deleteMessagesRes domain.DeleteMessagesResult
	deleteHistoryReq  domain.DeleteHistoryRequest
	deleteHistoryRes  domain.DeleteMessagesResult
	mediaReq          domain.MediaSearchRequest
}

type scheduledCaptureMessages struct {
	*captureMessages
	scheduled          []domain.ScheduledMessage
	scheduleReq        domain.ScheduleMessageRequest
	editScheduledReq   domain.EditScheduledMessageRequest
	claimScheduledReq  domain.ScheduledMessageClaim
	deletedScheduled   domain.ScheduledMessageFilter
	markedScheduledID  int
	markedSentID       int
	releasedScheduled  int
	releasedErrMessage string
}

type ttlCaptureMessages struct {
	*captureMessages
	setTTLUserID         int64
	setTTLPeer           domain.Peer
	setTTLPeriod         int
	defaultTTLUserID     int64
	defaultTTLPeriod     int
	expiredPrivateClaims []domain.DeleteMessagesRequest
}

func (s *ttlCaptureMessages) GetPrivateHistoryTTL(_ context.Context, _ int64, _ domain.Peer) (int, error) {
	return s.setTTLPeriod, nil
}

func (s *ttlCaptureMessages) SetPrivateHistoryTTL(_ context.Context, userID int64, peer domain.Peer, period int) error {
	s.setTTLUserID = userID
	s.setTTLPeer = peer
	s.setTTLPeriod = period
	return nil
}

func (s *ttlCaptureMessages) DefaultHistoryTTL(_ context.Context, userID int64) (int, error) {
	s.defaultTTLUserID = userID
	return s.defaultTTLPeriod, nil
}

func (s *ttlCaptureMessages) SetDefaultHistoryTTL(_ context.Context, userID int64, period int) error {
	s.defaultTTLUserID = userID
	s.defaultTTLPeriod = period
	return nil
}

func (s *ttlCaptureMessages) ClaimExpiredPrivateMessages(context.Context, int, int) ([]domain.DeleteMessagesRequest, error) {
	return append([]domain.DeleteMessagesRequest(nil), s.expiredPrivateClaims...), nil
}

// scheduledCaptureMessages 在 captureMessages 上叠一个按 postgres store 语义存取的定时队列
// （行与状态迁移见 scheduledRows），并记录各定时调用的请求供断言。
func (s *scheduledCaptureMessages) ScheduleMessage(_ context.Context, userID int64, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	s.scheduleReq = req
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return s.rows().schedule(req, len(s.scheduled)+41), nil
}

func (s *scheduledCaptureMessages) EditScheduledMessage(_ context.Context, userID int64, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	s.editScheduledReq = req
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return s.rows().edit(req)
}

func (s *scheduledCaptureMessages) ListScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	items := s.rows().match(filter.OwnerUserID, filter.Peer, nil, scheduledActiveStates...)
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (s *scheduledCaptureMessages) GetScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	items := s.rows().match(filter.OwnerUserID, filter.Peer, filter.IDs, scheduledActiveStates...)
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (s *scheduledCaptureMessages) DeleteScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter, _ int) ([]domain.ScheduledMessage, error) {
	if filter.OwnerUserID == 0 {
		filter.OwnerUserID = userID
	}
	s.deletedScheduled = filter
	return s.rows().transition(s.rows().match(filter.OwnerUserID, filter.Peer, filter.IDs, scheduledActiveStates...), "deleted"), nil
}

func (s *scheduledCaptureMessages) ClaimScheduledMessages(_ context.Context, userID int64, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if claim.OwnerUserID == 0 {
		claim.OwnerUserID = userID
	}
	s.claimScheduledReq = claim
	return s.rows().transition(s.rows().match(claim.OwnerUserID, claim.Peer, claim.IDs, "pending"), "dispatching"), nil
}

func (s *scheduledCaptureMessages) ClaimDueScheduledMessages(context.Context, int, int, int) ([]domain.ScheduledMessage, error) {
	return s.rows().claimDue(), nil
}

func (s *scheduledCaptureMessages) MarkScheduledMessageSent(_ context.Context, ownerUserID int64, id, sentMessageID, _ int) error {
	s.markedScheduledID = id
	s.markedSentID = sentMessageID
	s.rows().markSent(ownerUserID, id, sentMessageID)
	return nil
}

func (s *scheduledCaptureMessages) ReleaseScheduledMessage(_ context.Context, ownerUserID int64, id int, errText string) error {
	s.releasedScheduled = id
	s.releasedErrMessage = errText
	s.rows().release(ownerUserID, id)
	return nil
}

func (s *scheduledCaptureMessages) HasScheduledMessages(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	return len(s.rows().match(userID, peer, nil, scheduledActiveStates...)) > 0, nil
}

func (s *scheduledCaptureMessages) rows() *scheduledRows {
	return (*scheduledRows)(&s.scheduled)
}

// scheduledActiveStates 是 postgres 列表/删除/has_scheduled 认作"仍在队列里"的状态。
var scheduledActiveStates = []string{"pending", "dispatching"}

// scheduledRows 是 rpc 测试里定时队列的唯一实现，按 postgres store（scheduled_messages 表 +
// queries/scheduled_message.sql）的语义存取：内容按表里的列逐字段保存（表里没有列的字段丢弃），
// 行按 pending → dispatching → sent / deleted 迁移，发出与删除只改状态不删行。
// 与 postgres 唯一的差别：claimDue 不看 schedule_date 与重试退避，所有 pending 都视为到期。
type scheduledRows []domain.ScheduledMessage

func (rows *scheduledRows) schedule(req domain.ScheduleMessageRequest, id int) domain.ScheduledMessage {
	if req.RandomID != 0 {
		// 与 postgres 相同：同一 owner 仍在队列里的同 random_id 行原样返回（排程幂等）。
		for _, msg := range rows.match(req.OwnerUserID, domain.Peer{}, nil, scheduledActiveStates...) {
			if msg.RandomID == req.RandomID {
				return msg
			}
		}
	}
	msg := domain.ScheduledMessage{
		OwnerUserID:          req.OwnerUserID,
		ID:                   id,
		Peer:                 req.Peer,
		RandomID:             req.RandomID,
		Message:              req.Message,
		Entities:             append([]domain.MessageEntity(nil), req.Entities...),
		Media:                req.Media,
		RichMessage:          req.RichMessage,
		Silent:               req.Silent,
		NoForwards:           req.NoForwards,
		ReplyTo:              scheduledStoredReply(req.ReplyTo),
		Forward:              scheduledStoredForward(req.Forward),
		SendAs:               scheduledStoredSendAs(req.SendAs),
		GroupedID:            req.GroupedID,
		ScheduleDate:         req.ScheduleDate,
		ScheduleRepeatPeriod: req.ScheduleRepeatPeriod,
		CreatedAt:            req.Date,
		UpdatedAt:            req.Date,
		State:                "pending",
	}
	*rows = append(*rows, msg)
	return msg
}

// scheduledStoredReply 只保留 scheduled_messages 有列的回复字段（reply_to_* 与 quote_*）；
// 只指向 forum 话题的回复按 reply_to_top_id 还原。
func scheduledStoredReply(in *domain.MessageReply) *domain.MessageReply {
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
		QuoteEntities: append([]domain.MessageEntity(nil), in.QuoteEntities...),
		QuoteOffset:   in.QuoteOffset,
	}
}

// scheduledStoredForward 按 scheduled_messages 的 fwd_* 列还原转发头（含 channel_post、saved_from）。
func scheduledStoredForward(in *domain.MessageForward) *domain.MessageForward {
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

func scheduledStoredSendAs(in *domain.Peer) *domain.Peer {
	if in == nil || in.ID == 0 {
		return nil
	}
	out := *in
	return &out
}

func (rows *scheduledRows) edit(req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	for i := range *rows {
		msg := &(*rows)[i]
		if msg.OwnerUserID != req.OwnerUserID || msg.Peer != req.Peer || msg.ID != req.ID || msg.State != "pending" {
			continue
		}
		if req.SetMessage {
			msg.Message = req.Message
			msg.Entities = append([]domain.MessageEntity(nil), req.Entities...)
		}
		if req.SetRichMessage {
			msg.RichMessage = req.RichMessage
		}
		msg.ScheduleDate = req.ScheduleDate
		msg.UpdatedAt = req.Date
		return *msg, nil
	}
	return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
}

func (rows *scheduledRows) match(owner int64, peer domain.Peer, ids []int, states ...string) []domain.ScheduledMessage {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]domain.ScheduledMessage, 0, len(*rows))
	for _, msg := range *rows {
		if owner != 0 && msg.OwnerUserID != owner {
			continue
		}
		if peer.ID != 0 && msg.Peer != peer {
			continue
		}
		if len(want) > 0 && !want[msg.ID] {
			continue
		}
		for _, state := range states {
			if msg.State == state {
				out = append(out, msg)
				break
			}
		}
	}
	return out
}

func (rows *scheduledRows) transition(items []domain.ScheduledMessage, state string) []domain.ScheduledMessage {
	for i := range items {
		for j := range *rows {
			if (*rows)[j].OwnerUserID == items[i].OwnerUserID && (*rows)[j].ID == items[i].ID {
				(*rows)[j].State = state
				items[i].State = state
			}
		}
	}
	return items
}

func (rows *scheduledRows) claimDue() []domain.ScheduledMessage {
	return rows.transition(rows.match(0, domain.Peer{}, nil, "pending"), "dispatching")
}

func (rows *scheduledRows) markSent(ownerUserID int64, id, sentMessageID int) {
	for i := range *rows {
		if (*rows)[i].OwnerUserID == ownerUserID && (*rows)[i].ID == id {
			(*rows)[i].State = "sent"
			(*rows)[i].SentMessageID = sentMessageID
		}
	}
}

func (rows *scheduledRows) release(ownerUserID int64, id int) {
	for i := range *rows {
		if (*rows)[i].OwnerUserID == ownerUserID && (*rows)[i].ID == id && (*rows)[i].State == "dispatching" {
			(*rows)[i].State = "pending"
		}
	}
}

// scheduledQueue 是并发安全的 scheduledRows 包装，实现 scheduledMessagesService 的全部定时方法，
// 供"真实内存消息服务 + 定时队列"组合的测试替身嵌入。
type scheduledQueue struct {
	mu   sync.Mutex
	rows scheduledRows
}

func (q *scheduledQueue) ScheduleMessage(_ context.Context, userID int64, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return q.rows.schedule(req, len(q.rows)+1), nil
}

func (q *scheduledQueue) EditScheduledMessage(_ context.Context, userID int64, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	return q.rows.edit(req)
}

func (q *scheduledQueue) ListScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.rows.match(userID, filter.Peer, nil, scheduledActiveStates...)
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (q *scheduledQueue) GetScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.rows.match(userID, filter.Peer, filter.IDs, scheduledActiveStates...)
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (q *scheduledQueue) DeleteScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter, _ int) ([]domain.ScheduledMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rows.transition(q.rows.match(userID, filter.Peer, filter.IDs, scheduledActiveStates...), "deleted"), nil
}

func (q *scheduledQueue) ClaimScheduledMessages(_ context.Context, userID int64, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rows.transition(q.rows.match(userID, claim.Peer, claim.IDs, "pending"), "dispatching"), nil
}

func (q *scheduledQueue) ClaimDueScheduledMessages(context.Context, int, int, int) ([]domain.ScheduledMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rows.claimDue(), nil
}

func (q *scheduledQueue) MarkScheduledMessageSent(_ context.Context, ownerUserID int64, id, sentMessageID, _ int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rows.markSent(ownerUserID, id, sentMessageID)
	return nil
}

func (q *scheduledQueue) ReleaseScheduledMessage(_ context.Context, ownerUserID int64, id int, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rows.release(ownerUserID, id)
	return nil
}

func (q *scheduledQueue) HasScheduledMessages(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.rows.match(userID, peer, nil, scheduledActiveStates...)) > 0, nil
}

// snapshot 返回队列当前全部行（含已发出/已删除），供断言状态。
func (q *scheduledQueue) snapshot() []domain.ScheduledMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]domain.ScheduledMessage(nil), q.rows...)
}

func (s *captureMessages) SendPrivateText(_ context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	s.sendUserID = userID
	s.sendReq = req
	if s.sendResult.SenderMessage.ID == 0 {
		s.sendResult.SenderMessage = domain.Message{
			ID:          1,
			OwnerUserID: req.SenderUserID,
			RandomID:    req.RandomID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
			Date:        req.Date,
			Out:         true,
			Silent:      req.Silent,
			NoForwards:  req.NoForwards,
			Body:        req.Message,
			Entities:    req.Entities,
			Media:       req.Media,
			ReplyTo:     req.ReplyTo,
			Forward:     req.Forward,
			ViaBotID:    req.ViaBotID,
			Pts:         1,
		}
		s.sendResult.SenderEvent = domain.UpdateEvent{
			UserID:   req.SenderUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      1,
			PtsCount: 1,
			Date:     req.Date,
			Message:  s.sendResult.SenderMessage,
		}
	}
	return s.sendResult, nil
}

func (s *captureMessages) SetChatTheme(_ context.Context, userID int64, req domain.SetPrivateChatThemeRequest) (domain.SetPrivateChatThemeResult, error) {
	s.setThemeUserID = userID
	s.setThemeReq = req
	if s.setThemeRes.OwnerUserID == 0 {
		s.setThemeRes = domain.SetPrivateChatThemeResult{
			OwnerUserID: userID,
			Peer:        req.Peer,
			Emoticon:    req.Emoticon,
			Changed:     true,
			Send: domain.SendPrivateTextResult{
				SenderMessage: domain.Message{
					ID:          1,
					OwnerUserID: userID,
					Peer:        req.Peer,
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: userID},
					Date:        req.Date,
					Out:         true,
					Media: &domain.MessageMedia{
						Kind: domain.MessageMediaKindService,
						ServiceAction: &domain.MessageServiceAction{
							Kind:              domain.MessageServiceActionSetChatTheme,
							ChatThemeEmoticon: req.Emoticon,
						},
					},
					Pts: 1,
				},
				SenderEvent: domain.UpdateEvent{
					UserID:   userID,
					Type:     domain.UpdateEventNewMessage,
					Pts:      1,
					PtsCount: 1,
					Date:     req.Date,
				},
			},
		}
		s.setThemeRes.Send.SenderEvent.Message = s.setThemeRes.Send.SenderMessage
	}
	return s.setThemeRes, nil
}

func (s *captureMessages) ForwardPrivateMessages(_ context.Context, userID int64, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	s.forwardUserID = userID
	s.forwardReq = req
	if len(s.forwardRes.SenderMessages) == 0 {
		s.forwardRes.OwnerUserID = userID
		s.forwardRes.SenderMessages = make([]domain.Message, 0, len(req.MessageIDs))
		s.forwardRes.SenderEvents = make([]domain.UpdateEvent, 0, len(req.MessageIDs))
		for i := range req.MessageIDs {
			msg := domain.Message{
				ID:          i + 1,
				OwnerUserID: req.OwnerUserID,
				RandomID:    req.RandomIDs[i],
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.ToUserID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
				Date:        req.Date,
				Out:         true,
				Silent:      req.Silent,
				NoForwards:  req.NoForwards,
				Body:        "forwarded",
				ReplyTo:     req.ReplyTo,
				Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: req.FromPeer.ID}, Date: req.Date - 1},
				Pts:         i + 1,
			}
			event := domain.UpdateEvent{
				UserID:   req.OwnerUserID,
				Type:     domain.UpdateEventNewMessage,
				Pts:      msg.Pts,
				PtsCount: 1,
				Date:     req.Date,
				Message:  msg,
			}
			s.forwardRes.SenderMessages = append(s.forwardRes.SenderMessages, msg)
			s.forwardRes.SenderEvents = append(s.forwardRes.SenderEvents, event)
		}
	}
	return s.forwardRes, nil
}

func (s *captureMessages) GetMessages(_ context.Context, _ int64, ids []int) (domain.MessageList, error) {
	s.getMessagesCalls++
	s.getMessagesIDs = append(s.getMessagesIDs, append([]int(nil), ids...))
	byID := make(map[int]domain.Message, len(s.list.Messages))
	for _, msg := range s.list.Messages {
		byID[msg.ID] = msg
	}
	out := domain.MessageList{Messages: make([]domain.Message, 0, len(ids)), Users: s.list.Users}
	if s.getMessagesListed {
		for _, msg := range s.list.Messages {
			for _, id := range ids {
				if msg.ID == id {
					out.Messages = append(out.Messages, msg)
					break
				}
			}
		}
		return out, nil
	}
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	return out, nil
}

func (s *captureMessages) GetHistory(_ context.Context, _ int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureMessages) Search(_ context.Context, _ int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureMessages) SearchPrivateMedia(_ context.Context, _, _ int64, req domain.MediaSearchRequest) (domain.MessageList, error) {
	s.mediaReq = req
	return domain.MessageList{}, nil
}

func (s *captureMessages) CountPrivateMediaCategories(_ context.Context, _, _ int64) (domain.MediaCategoryCounts, error) {
	return domain.MediaCategoryCounts{}, nil
}

func (s *captureMessages) ReadHistory(_ context.Context, _ int64, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	s.readReq = req
	s.readPeer = req.Peer
	s.readMaxID = req.MaxID
	if s.readResult.OwnerUserID == 0 {
		s.readResult.OwnerUserID = req.OwnerUserID
	}
	if s.readResult.Peer.ID == 0 {
		s.readResult.Peer = req.Peer
	}
	if s.readResult.MaxID == 0 {
		s.readResult.MaxID = req.MaxID
	}
	return s.readResult, nil
}

func (s *captureMessages) ReadMessageContents(_ context.Context, userID int64, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	s.readContentsReq = req
	if s.readContentsRes.OwnerUserID == 0 {
		s.readContentsRes.OwnerUserID = userID
	}
	return s.readContentsRes, nil
}

func (s *captureMessages) GetOutboxReadDate(_ context.Context, _ int64, req domain.OutboxReadDateRequest) (int, error) {
	s.outboxReadDateReq = req
	if s.outboxReadDate == 0 {
		return 0, domain.ErrMessageNotReadYet
	}
	return s.outboxReadDate, nil
}

func (s *captureMessages) SetMessageReactions(_ context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.setReactionReq = req
	if len(s.setReactionRes.Messages) == 0 {
		if len(req.Reactions) == 0 {
			reactions := domain.ChannelMessageReactions{
				CanSeeList: req.Peer.ID != userID,
				AsTags:     req.Peer.ID == userID,
				Results:    []domain.ChannelMessageReactionCount{},
				Recent:     []domain.ChannelMessagePeerReaction{},
			}
			s.setReactionRes = domain.PrivateMessageReactionsResult{
				Messages: []domain.Message{{
					ID:          req.MessageID,
					OwnerUserID: userID,
					Peer:        req.Peer,
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
					Date:        req.Date,
					Reactions:   &reactions,
				}},
				Reactions: reactions,
			}
			return s.setReactionRes, nil
		}
		reactions := domain.ChannelMessageReactions{
			CanSeeList: req.Peer.ID != userID,
			AsTags:     req.Peer.ID == userID,
			Results: []domain.ChannelMessageReactionCount{{
				Reaction:    req.Reactions[0],
				Count:       1,
				ChosenOrder: 1,
			}},
		}
		if req.Peer.ID != userID {
			reactions.Recent = []domain.ChannelMessagePeerReaction{{
				UserID:      userID,
				Reaction:    req.Reactions[0],
				My:          true,
				Big:         req.Big,
				ChosenOrder: 1,
				Date:        req.Date,
			}}
		}
		s.setReactionRes = domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{{
				ID:          req.MessageID,
				OwnerUserID: userID,
				Peer:        req.Peer,
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
				Date:        req.Date,
				Reactions:   &reactions,
			}},
			Reactions: reactions,
		}
	}
	return s.setReactionRes, nil
}

func (s *captureMessages) VoteMessagePoll(_ context.Context, _ int64, _ domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
}

func (s *captureMessages) CloseMessagePoll(_ context.Context, _ int64, _ domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error) {
	return domain.PrivateMessagePollResult{}, domain.ErrMessageIDInvalid
}

func (s *captureMessages) GetMessageReactions(_ context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.getReactionReq = req
	if len(s.getReactionRes.Messages) == 0 && len(req.IDs) > 0 {
		reactions := domain.ChannelMessageReactions{CanSeeList: true, Results: []domain.ChannelMessageReactionCount{}, Recent: []domain.ChannelMessagePeerReaction{}}
		s.getReactionRes = domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{{
				ID:          req.IDs[0],
				OwnerUserID: userID,
				Peer:        req.Peer,
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
				Reactions:   &reactions,
			}},
			Reactions: reactions,
		}
	}
	return s.getReactionRes, nil
}

func (s *captureMessages) SavedReactionTags(_ context.Context, _ int64, savedPeer domain.Peer, _ int) ([]domain.SavedReactionTag, error) {
	s.savedTagPeer = savedPeer
	return append([]domain.SavedReactionTag(nil), s.savedTags...), s.savedTagErr
}

func (s *captureMessages) UpdateSavedReactionTag(_ context.Context, _ int64, tag domain.SavedReactionTag) error {
	s.updatedSavedTag = tag
	for i := range s.savedTags {
		if s.savedTags[i].Reaction.Key() == tag.Reaction.Key() {
			s.savedTags[i].Title = tag.Title
			return s.savedTagErr
		}
	}
	return s.savedTagErr
}

func (s *captureMessages) EditMessage(_ context.Context, userID int64, req domain.EditMessageRequest) (domain.EditMessageResult, error) {
	s.editReq = req
	if s.editRes.OwnerUserID == 0 {
		s.editRes.OwnerUserID = userID
	}
	if len(s.editRes.Edited) == 0 {
		msg := domain.Message{
			ID:          req.ID,
			OwnerUserID: req.OwnerUserID,
			Peer:        req.Peer,
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
			Date:        req.EditDate - 10,
			EditDate:    req.EditDate,
			Out:         true,
			Body:        req.Message,
			Entities:    append([]domain.MessageEntity(nil), req.Entities...),
			Pts:         7,
		}
		s.editRes.Edited = []domain.EditedMessageForUser{{
			UserID:  req.OwnerUserID,
			Message: msg,
			Event: domain.UpdateEvent{
				UserID:   req.OwnerUserID,
				Type:     domain.UpdateEventEditMessage,
				Pts:      7,
				PtsCount: 1,
				Date:     req.EditDate,
				Message:  msg,
			},
		}}
	}
	return s.editRes, nil
}

func (s *captureMessages) DeleteMessages(_ context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	s.deleteMessagesReq = req
	if s.deleteMessagesRes.OwnerUserID == 0 {
		s.deleteMessagesRes.OwnerUserID = userID
	}
	return s.deleteMessagesRes, nil
}

func (s *captureMessages) GetSavedDialogs(_ context.Context, _ int64, _ domain.SavedDialogsFilter) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s *captureMessages) GetPinnedSavedDialogs(_ context.Context, _ int64) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s *captureMessages) GetSavedDialogsByPeers(_ context.Context, _ int64, _ []domain.Peer) (domain.SavedDialogList, error) {
	return domain.SavedDialogList{}, nil
}

func (s *captureMessages) ToggleSavedDialogPin(_ context.Context, _ int64, _ domain.Peer, _ bool) (bool, error) {
	return false, nil
}

func (s *captureMessages) ReorderPinnedSavedDialogs(_ context.Context, _ int64, _ []domain.Peer, _ bool) error {
	return nil
}

func (s *captureMessages) DeleteSavedHistory(_ context.Context, _ int64, _ domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error) {
	return domain.DeleteSavedHistoryResult{}, nil
}

func (s *captureMessages) DeleteHistory(_ context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	s.deleteHistoryReq = req
	if s.deleteHistoryRes.OwnerUserID == 0 {
		s.deleteHistoryRes.OwnerUserID = userID
	}
	return s.deleteHistoryRes, nil
}

func (s *captureMessages) PinPrivateMessage(_ context.Context, userID int64, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error) {
	return domain.PinPrivateMessageResult{OwnerUserID: userID}, nil
}

func (s *captureMessages) UnpinAllPrivateMessages(_ context.Context, userID int64, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error) {
	return domain.PinPrivateMessageResult{OwnerUserID: userID}, nil
}

func (s *captureMessages) ListUnreadReactionMessages(_ context.Context, _ int64, _ domain.Peer, _ int) ([]domain.Message, error) {
	return nil, nil
}

func (s *captureMessages) ReadPeerReactions(_ context.Context, _ int64, _ domain.Peer) (int, error) {
	return 0, nil
}
