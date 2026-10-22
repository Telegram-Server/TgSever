package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

const (
	botAPIGetUpdatesLimit   = 100
	botAPIMaxNegativeOffset = 10000
)

type botAPIChannelBotMemberProvider interface {
	ActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error)
}

func (r *Router) botAPIQueuedUpdates(ctx context.Context, botID int64, offset int64) ([]domain.UpdateEvent, error) {
	if r == nil || r.deps.BotAPIUpdates == nil || botID == 0 {
		return nil, nil
	}
	fromID := int64(1)
	var items []domain.BotAPIUpdate
	if offset < 0 {
		if offset < -botAPIMaxNegativeOffset {
			return nil, errors.New("OFFSET_INVALID")
		}
		var err error
		items, err = r.deps.BotAPIUpdates.ListTailBotAPIUpdates(ctx, botID, int(-offset), botAPIGetUpdatesLimit)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 && items[0].ID > 1 {
			if err := r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, items[0].ID-1); err != nil {
				return nil, err
			}
		}
	} else if offset > 0 {
		if err := r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, offset-1); err != nil {
			return nil, err
		}
	}
	if offset >= 0 {
		confirmed, found, err := r.deps.BotAPIUpdates.ConfirmedBotAPIUpdateID(ctx, botID)
		if err != nil {
			return nil, err
		}
		if found {
			fromID = confirmed + 1
		}
		items, err = r.deps.BotAPIUpdates.ListBotAPIUpdates(ctx, botID, fromID, botAPIGetUpdatesLimit)
		if err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	events, leadingSkipped := r.botAPIQueuedUpdateEvents(ctx, botID, items, r.clock.Now())
	if leadingSkipped > 0 {
		if err := r.deps.BotAPIUpdates.ConfirmBotAPIUpdates(ctx, botID, leadingSkipped); err != nil {
			return nil, err
		}
	}
	if len(events) == 0 {
		return nil, nil
	}
	return r.enrichUpdateEvents(ctx, botID, events), nil
}

func (r *Router) botAPIQueuedUpdateEvents(ctx context.Context, botID int64, items []domain.BotAPIUpdate, now time.Time) ([]domain.UpdateEvent, int64) {
	privateIDs := make([]int, 0)
	privateSeen := make(map[int]struct{})
	channelIDs := make(map[int64][]int)
	channelSeen := make(map[int64]map[int]struct{})
	for _, item := range items {
		if _, ok := botAPIQueuedUpdateKind(botID, item, now); !ok {
			continue
		}
		if item.Object != nil || item.Ephemeral != nil || item.ChatMember != nil || item.Reaction != nil || item.JoinRequest != nil {
			continue
		}
		if item.Callback != nil && item.Callback.InlineMessage != nil {
			continue
		}
		switch item.Peer.Type {
		case domain.PeerTypeUser:
			if _, exists := privateSeen[item.MessageID]; !exists {
				privateSeen[item.MessageID] = struct{}{}
				privateIDs = append(privateIDs, item.MessageID)
			}
		case domain.PeerTypeChannel:
			seen := channelSeen[item.Peer.ID]
			if seen == nil {
				seen = make(map[int]struct{})
				channelSeen[item.Peer.ID] = seen
			}
			if _, exists := seen[item.MessageID]; !exists {
				seen[item.MessageID] = struct{}{}
				channelIDs[item.Peer.ID] = append(channelIDs[item.Peer.ID], item.MessageID)
			}
		}
	}

	privateMessages := r.botAPIQueuedPrivateMessages(ctx, botID, privateIDs)
	channelMessages := r.botAPIQueuedChannelMessages(ctx, botID, channelIDs)
	events := make([]domain.UpdateEvent, 0, len(items))
	leadingSkipped := int64(0)
	for _, item := range items {
		event, ok := botAPIQueuedUpdateEventFromMessages(botID, item, privateMessages, channelMessages, now)
		if !ok || !r.botAPIObjectAuthorized(ctx, botID, item) {
			if len(events) == 0 {
				leadingSkipped = item.ID
			}
			continue
		}
		events = append(events, event)
	}
	return events, leadingSkipped
}

func (r *Router) botAPIQueuedPrivateMessages(ctx context.Context, botID int64, ids []int) map[int]domain.Message {
	if r == nil || r.deps.Messages == nil || len(ids) == 0 {
		return nil
	}
	list, err := r.deps.Messages.GetMessages(ctx, botID, ids)
	if err != nil {
		return nil
	}
	out := make(map[int]domain.Message, len(list.Messages))
	for _, msg := range list.Messages {
		if msg.ID <= 0 || msg.OwnerUserID != botID {
			continue
		}
		out[msg.ID] = msg
	}
	return out
}

func (r *Router) botAPIQueuedChannelMessages(ctx context.Context, botID int64, idsByChannel map[int64][]int) map[int64]map[int]domain.ChannelMessage {
	if r == nil || r.deps.Channels == nil || len(idsByChannel) == 0 {
		return nil
	}
	out := make(map[int64]map[int]domain.ChannelMessage, len(idsByChannel))
	for channelID, ids := range idsByChannel {
		if channelID == 0 || len(ids) == 0 {
			continue
		}
		history, err := r.deps.Channels.GetMessages(ctx, botID, channelID, ids)
		if err != nil {
			continue
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, msg := range history.Messages {
			if msg.ID <= 0 || msg.Deleted || msg.Action != nil {
				continue
			}
			byID[msg.ID] = msg
		}
		if len(byID) > 0 {
			out[channelID] = byID
		}
	}
	return out
}

func botAPIQueuedUpdateKind(botID int64, item domain.BotAPIUpdate, now time.Time) (domain.UpdateEventType, bool) {
	if item.ID <= 0 || item.BotUserID != botID {
		return "", false
	}
	eventType, ok := botAPIUpdateEventType(item.Kind)
	if !ok {
		return "", false
	}
	if item.Object != nil {
		err := domain.ValidateBotAPIObjectRequest(domain.EnqueueBotAPIUpdateRequest{BotUserID: item.BotUserID, Kind: item.Kind, Peer: item.Peer, MessageID: item.MessageID, SourcePts: item.SourcePts, Date: item.Date, Object: item.Object, Callback: item.Callback, PreCheckout: item.PreCheckout, Ephemeral: item.Ephemeral, ChatMember: item.ChatMember, Reaction: item.Reaction, JoinRequest: item.JoinRequest})
		return domain.UpdateEventBotAPIObject, err == nil && now.Before(time.Unix(int64(item.Date), 0).Add(24*time.Hour))
	}
	if item.Ephemeral != nil && !botAPIQueuedEphemeralValid(botID, item, now) {
		return "", false
	}
	if item.Kind == domain.BotAPIUpdateChatJoinRequest {
		err := domain.ValidateBotAPIJoinRequest(domain.EnqueueBotAPIUpdateRequest{BotUserID: botID, Kind: item.Kind, Peer: item.Peer, MessageID: item.MessageID, SourcePts: item.SourcePts, Date: item.Date, JoinRequest: item.JoinRequest})
		return eventType, err == nil
	}
	if item.JoinRequest != nil {
		return "", false
	}
	if item.Kind == domain.BotAPIUpdateMessageReaction || item.Kind == domain.BotAPIUpdateMessageReactionCount {
		err := domain.ValidateBotAPIReactionRequest(domain.EnqueueBotAPIUpdateRequest{BotUserID: botID, Kind: item.Kind, Peer: item.Peer, MessageID: item.MessageID, Date: item.Date, Reaction: item.Reaction})
		return eventType, err == nil
	}
	if item.Reaction != nil {
		return "", false
	}
	if item.Kind == domain.BotAPIUpdateMyChatMember || item.Kind == domain.BotAPIUpdateChatMember {
		// 成员更新没有消息：载荷自足，只核对它与队列行一致。
		if item.ChatMember == nil || item.MessageID != 0 || item.Peer.Type != domain.PeerTypeChannel ||
			item.Peer != item.ChatMember.Chat || item.ChatMember.ValidateFor(botID, item.Kind) != nil {
			return "", false
		}
		return eventType, true
	}
	if item.ChatMember != nil {
		return "", false
	}
	if item.Kind == domain.BotAPIUpdatePreCheckoutQuery {
		query := item.PreCheckout
		if query == nil || query.ID == 0 || query.BotUserID != botID || query.UserID <= 0 ||
			query.Currency == "" || query.TotalAmount <= 0 || item.Date <= 0 {
			return "", false
		}
		// The gate lives 10 seconds, so a queued question that was never read is
		// worthless: letting it through would only deliver a query whose window
		// has already closed and confuse the bot into answering a stale id.
		if !now.Before(time.Unix(int64(item.Date), 0).Add(preCheckoutTimeout)) {
			return "", false
		}
		// Returned here rather than falling through: the checks below require a
		// message id and a peer type, and a pre-checkout question has neither by
		// design. Letting it fall through rejected every question after it had
		// already been validated, which is why the bot never saw one.
		return eventType, true
	}
	if item.Kind == domain.BotAPIUpdateCallbackQuery {
		if item.Date <= 0 || !now.Before(time.Unix(int64(item.Date), 0).Add(botCallbackTimeout)) {
			return "", false
		}
		cb := item.Callback
		if cb == nil || cb.ID == 0 || cb.BotUserID != botID || cb.UserID <= 0 ||
			cb.ChatInstance == 0 || len(cb.Data) > domain.MaxCallbackDataLen {
			return "", false
		}
		if cb.InlineMessage != nil {
			inline := cb.InlineMessage
			if item.MessageID != 0 || item.Peer != (domain.Peer{}) || cb.MessageID != 0 || cb.Peer != (domain.Peer{}) ||
				inline.DCID <= 0 || inline.OwnerID == 0 || inline.ID <= 0 || inline.AccessHash == 0 {
				return "", false
			}
			return eventType, true
		}
		if item.MessageID <= 0 || cb.Peer != item.Peer || cb.MessageID != item.MessageID {
			return "", false
		}
	} else if item.MessageID <= 0 {
		return "", false
	}
	switch item.Peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if item.Peer.ID <= 0 {
			return "", false
		}
	default:
		return "", false
	}
	return eventType, true
}

func botAPIQueuedEphemeralValid(botID int64, item domain.BotAPIUpdate, now time.Time) bool {
	if item.Ephemeral == nil {
		return true
	}
	message := item.Ephemeral.Message
	if item.Ephemeral.Validate() != nil || item.SourcePts != 0 || item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID <= 0 ||
		message.ID != item.MessageID || message.Peer != item.Peer || message.Expired(now) ||
		message.SenderUserID <= 0 || message.ReceiverUserID <= 0 {
		return false
	}
	if item.Kind == domain.BotAPIUpdateCallbackQuery {
		return message.SenderUserID == botID
	}
	return message.ReceiverUserID == botID
}

func botAPIQueuedUpdateEventFromMessages(botID int64, item domain.BotAPIUpdate, privateMessages map[int]domain.Message, channelMessages map[int64]map[int]domain.ChannelMessage, now time.Time) (domain.UpdateEvent, bool) {
	eventType, ok := botAPIQueuedUpdateKind(botID, item, now)
	if !ok {
		return domain.UpdateEvent{}, false
	}
	if item.Object != nil {
		return domain.UpdateEvent{UserID: botID, Type: eventType, BotAPIUpdateID: item.ID, Date: item.Date, BotAPIObject: item.Object.Clone()}, true
	}
	if item.JoinRequest != nil {
		peers := []domain.Peer{{Type: domain.PeerTypeUser, ID: item.JoinRequest.UserID}}
		if item.JoinRequest.Invite != nil {
			peers = append(peers, domain.Peer{Type: domain.PeerTypeUser, ID: item.JoinRequest.Invite.AdminUserID})
		}
		return domain.UpdateEvent{UserID: botID, Type: eventType, BotAPIUpdateID: item.ID, Date: item.Date, Peer: item.Peer, Peers: peers, BotJoinRequest: item.JoinRequest.Clone()}, true
	}
	if item.Reaction != nil {
		return domain.UpdateEvent{UserID: botID, Type: eventType, BotAPIUpdateID: item.ID, Date: item.Date, Peer: item.Peer, Peers: []domain.Peer{item.Reaction.Actor()}, BotReaction: item.Reaction.Clone()}, true
	}
	if item.Ephemeral != nil {
		message := item.Ephemeral.EphemeralMessage()
		event := domain.UpdateEvent{
			UserID: botID, Type: eventType, Date: item.Date, Peer: item.Peer,
			BotAPIUpdateID: item.ID, EphemeralMessage: &message,
		}
		if eventType == domain.UpdateEventBotCallbackQuery {
			callback := *item.Callback
			callback.Data = append([]byte(nil), item.Callback.Data...)
			event.BotCallbackQuery = &callback
		}
		return event, true
	}
	if eventType == domain.UpdateEventBotMyChatMember || eventType == domain.UpdateEventBotChatMember {
		update := *item.ChatMember
		// 当事人、操作者、邀请人放进 Peers，由 enrichUpdateEvents 补齐用户对象；
		// Peer 指向群/频道，同样会被补齐。
		peers := []domain.Peer{
			{Type: domain.PeerTypeUser, ID: update.ActorUserID},
			{Type: domain.PeerTypeUser, ID: update.New.UserID},
		}
		for _, inviter := range []int64{update.Old.InviterUserID, update.New.InviterUserID} {
			if inviter > 0 {
				peers = append(peers, domain.Peer{Type: domain.PeerTypeUser, ID: inviter})
			}
		}
		if update.Invite != nil {
			peers = append(peers, domain.Peer{Type: domain.PeerTypeUser, ID: update.Invite.AdminUserID})
		}
		return domain.UpdateEvent{
			UserID:         botID,
			Type:           eventType,
			BotAPIUpdateID: item.ID,
			Date:           item.Date,
			Peer:           item.Peer,
			Peers:          peers,
			BotChatMember:  &update,
		}, true
	}
	// A pre-checkout query is not about any message: it carries only the order
	// fields, with no peer and no message id. It therefore has to be handled
	// before the peer switch below, which would drop it for having neither.
	if eventType == domain.UpdateEventBotPreCheckoutQuery {
		query := item.PreCheckout
		if query == nil {
			return domain.UpdateEvent{}, false
		}
		preCheckout := *query
		return domain.UpdateEvent{
			UserID:         botID,
			Type:           eventType,
			BotAPIUpdateID: item.ID,
			Date:           item.Date,
			BotPreCheckout: &preCheckout,
		}, true
	}
	if eventType == domain.UpdateEventBotCallbackQuery && item.Callback.InlineMessage != nil {
		callback := *item.Callback
		callback.Data = append([]byte(nil), item.Callback.Data...)
		inline := *item.Callback.InlineMessage
		callback.InlineMessage = &inline
		return domain.UpdateEvent{
			UserID:           botID,
			Type:             eventType,
			Pts:              int(item.ID),
			PtsCount:         1,
			BotAPIUpdateID:   item.ID,
			Date:             item.Date,
			BotCallbackQuery: &callback,
		}, true
	}
	switch item.Peer.Type {
	case domain.PeerTypeUser:
		msg, found := privateMessages[item.MessageID]
		if !found {
			return domain.UpdateEvent{}, false
		}
		if eventType == domain.UpdateEventBotCallbackQuery {
			callback := *item.Callback
			callback.Data = append([]byte(nil), item.Callback.Data...)
			return domain.UpdateEvent{
				UserID:           botID,
				Type:             eventType,
				Pts:              int(item.ID),
				PtsCount:         1,
				BotAPIUpdateID:   item.ID,
				Date:             item.Date,
				Peer:             item.Peer,
				Message:          msg,
				BotCallbackQuery: &callback,
			}, true
		}
		if (msg.Out && eventType != domain.UpdateEventEditMessage) || !botAPIMessageProjectable(msg) {
			return domain.UpdateEvent{}, false
		}
		msg.Pts = int(item.ID)
		return domain.UpdateEvent{
			UserID:         botID,
			Type:           eventType,
			Pts:            int(item.ID),
			PtsCount:       1,
			BotAPIUpdateID: item.ID,
			Date:           item.Date,
			Peer:           msg.Peer,
			Message:        msg,
		}, true
	case domain.PeerTypeChannel:
		msg, found := channelMessages[item.Peer.ID][item.MessageID]
		if !found {
			return domain.UpdateEvent{}, false
		}
		projected := botAPIMessageFromChannel(botID, msg)
		if eventType == domain.UpdateEventBotCallbackQuery {
			callback := *item.Callback
			callback.Data = append([]byte(nil), item.Callback.Data...)
			return domain.UpdateEvent{
				UserID:           botID,
				Type:             eventType,
				Pts:              int(item.ID),
				PtsCount:         1,
				BotAPIUpdateID:   item.ID,
				Date:             item.Date,
				Peer:             item.Peer,
				Message:          projected,
				BotCallbackQuery: &callback,
			}, true
		}
		if projected.Out || !botAPIMessageProjectable(projected) {
			return domain.UpdateEvent{}, false
		}
		projected.Pts = int(item.ID)
		return domain.UpdateEvent{
			UserID:         botID,
			Type:           eventType,
			Pts:            int(item.ID),
			PtsCount:       1,
			BotAPIUpdateID: item.ID,
			Date:           item.Date,
			Peer:           projected.Peer,
			Message:        projected,
		}, true
	default:
		return domain.UpdateEvent{}, false
	}
}

func botAPIUpdateEventType(kind domain.BotAPIUpdateKind) (domain.UpdateEventType, bool) {
	switch kind {
	case domain.BotAPIUpdateMessage:
		return domain.UpdateEventNewMessage, true
	case domain.BotAPIUpdateEditedMessage:
		return domain.UpdateEventEditMessage, true
	case domain.BotAPIUpdateCallbackQuery:
		return domain.UpdateEventBotCallbackQuery, true
	case domain.BotAPIUpdatePreCheckoutQuery:
		return domain.UpdateEventBotPreCheckoutQuery, true
	case domain.BotAPIUpdateMyChatMember:
		return domain.UpdateEventBotMyChatMember, true
	case domain.BotAPIUpdateChatMember:
		return domain.UpdateEventBotChatMember, true
	case domain.BotAPIUpdateMessageReaction:
		return domain.UpdateEventBotMessageReaction, true
	case domain.BotAPIUpdateMessageReactionCount:
		return domain.UpdateEventBotMessageReactionCount, true
	case domain.BotAPIUpdateChatJoinRequest:
		return domain.UpdateEventBotChatJoinRequest, true
	default:
		if domain.BotAPIObjectKind(kind) {
			return domain.UpdateEventBotAPIObject, true
		}
		return "", false
	}
}

// enqueueBotAPIObject snapshots a public Bot API object before the producer can
// mutate it. The existing durable queue owns filtering, acknowledgement and TTL.
func (r *Router) enqueueBotAPIObject(ctx context.Context, botID int64, kind domain.BotAPIUpdateKind, payload map[string]any) {
	if r == nil || r.deps.BotAPIUpdates == nil || botID <= 0 {
		return
	}
	object, err := domain.NewBotAPIObject(kind, payload)
	if err != nil {
		return
	}
	date := int(r.clock.Now().Unix())
	r.botAPIEnqueueQueue.Enqueue(ctx, func(jobCtx context.Context) {
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(jobCtx, domain.EnqueueBotAPIUpdateRequest{BotUserID: botID, Kind: kind, Date: date, Object: object}); err == nil && created {
			r.notifyBotAPIUpdate(botID)
		}
	})
}

// Snapshot before dispatch so a subsequent edit cannot rewrite an unread update.
func (r *Router) enqueueBotAPIPrivateMessageUpdateAsync(ctx context.Context, res domain.SendPrivateTextResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || res.Duplicate || res.RecipientMessage.ID <= 0 {
		return
	}
	r.enqueueBotAPIPrivateMessageUpdate(ctx, res)
}

// enqueueBotAPIPrivateEditUpdatesAsync 同上，覆盖私聊编辑的 edited_message 队列写入。
func (r *Router) enqueueBotAPIPrivateEditUpdatesAsync(ctx context.Context, res domain.EditMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || len(res.Edited) == 0 {
		return
	}
	r.enqueueBotAPIPrivateEditUpdates(ctx, res)
}

func (r *Router) enqueueBotAPIPrivateMessageUpdate(ctx context.Context, res domain.SendPrivateTextResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || res.Duplicate || res.RecipientMessage.ID <= 0 || res.RecipientMessage.Out {
		return
	}
	r.enqueueBotAPIBusinessPrivateMessage(ctx, res)
	botID := res.RecipientMessage.OwnerUserID
	if botID == 0 || !botAPIMessageProjectable(res.RecipientMessage) {
		return
	}
	isBot, err := r.botAPIKnownBot(ctx, botID)
	if err != nil || !isBot {
		return
	}
	if res.SenderMessage.OwnerUserID > 0 && r.ensureBotAccess(ctx, botID, res.SenderMessage.OwnerUserID) != nil {
		return
	}
	r.enqueueBotAPIMessageSnapshot(ctx, botID, domain.BotAPIUpdateMessage, res.RecipientMessage, nil)
}

func (r *Router) enqueueBotAPIPrivateEditUpdates(ctx context.Context, res domain.EditMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil {
		return
	}
	r.enqueueBotAPIBusinessPrivateEdits(ctx, res)
	for _, item := range res.Edited {
		if item.UserID == 0 || item.UserID == res.OwnerUserID || !botAPIMessageProjectable(item.Message) {
			continue
		}
		isBot, err := r.botAPIKnownBot(ctx, item.UserID)
		if err != nil || !isBot {
			continue
		}
		if res.OwnerUserID > 0 && r.ensureBotAccess(ctx, item.UserID, res.OwnerUserID) != nil {
			continue
		}
		r.enqueueBotAPIMessageSnapshot(ctx, item.UserID, domain.BotAPIUpdateEditedMessage, item.Message, nil)
	}
}

func (r *Router) enqueueBotAPIChannelMessageUpdate(ctx context.Context, originUserID int64, res domain.SendChannelMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Channels == nil || res.Duplicate || res.Message.ID <= 0 || res.Message.ChannelID == 0 {
		return
	}
	botIDs, err := r.botAPIChannelBotCandidates(ctx, originUserID, res.Message.ChannelID)
	if err != nil {
		return
	}
	r.enqueueBotAPIChannelMessageUpdateForBots(ctx, res, botIDs)
}

func (r *Router) enqueueBotAPIChannelMessageUpdateForBots(ctx context.Context, res domain.SendChannelMessageResult, botIDs []int64) {
	if r == nil || r.deps.BotAPIUpdates == nil || res.Duplicate || res.Message.ID <= 0 || res.Message.ChannelID == 0 || len(botIDs) == 0 {
		return
	}
	skip := skipDeliverySet(res.SkipDeliveryUserIDs)
	for _, botID := range botIDs {
		if botID == 0 || botID == res.Message.SenderUserID {
			continue
		}
		if _, skipped := skip[botID]; skipped {
			continue
		}
		if res.Message.SenderUserID > 0 && r.ensureBotAccess(ctx, botID, res.Message.SenderUserID) != nil {
			continue
		}
		message := botAPIMessageFromChannel(botID, res.Message)
		kind := domain.BotAPIUpdateMessage
		view, err := r.deps.Channels.GetChannel(ctx, botID, res.Message.ChannelID)
		if err != nil || view.Forbidden || view.Self.Status != domain.ChannelMemberActive {
			continue
		}
		if view.Channel.Broadcast && !view.Channel.Megagroup {
			kind = domain.BotAPIUpdateKind("channel_post")
		}
		r.enqueueBotAPIMessageSnapshot(ctx, botID, kind, message, []domain.Channel{view.Channel})
	}
}

func (r *Router) enqueueBotAPIChannelMessagesUpdate(ctx context.Context, originUserID int64, results []domain.SendChannelMessageResult) {
	candidates := make(map[int64][]int64)
	for _, res := range results {
		if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Channels == nil || res.Duplicate || res.Message.ID <= 0 || res.Message.ChannelID == 0 {
			continue
		}
		botIDs, ok := candidates[res.Message.ChannelID]
		if !ok {
			loaded, err := r.botAPIChannelBotCandidates(ctx, originUserID, res.Message.ChannelID)
			if err != nil {
				candidates[res.Message.ChannelID] = nil
				continue
			}
			botIDs = loaded
			candidates[res.Message.ChannelID] = botIDs
		}
		r.enqueueBotAPIChannelMessageUpdateForBots(ctx, res, botIDs)
	}
}

func (r *Router) enqueueBotAPIChannelEditMessageUpdate(ctx context.Context, originUserID int64, res domain.EditChannelMessageResult) {
	if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Channels == nil || res.Message.ID <= 0 || res.Message.ChannelID == 0 || res.Event.Pts == 0 {
		return
	}
	botIDs, err := r.botAPIChannelBotCandidates(ctx, originUserID, res.Message.ChannelID)
	if err != nil {
		return
	}
	for _, botID := range botIDs {
		if botID == 0 || botID == res.Message.SenderUserID {
			continue
		}
		if res.Message.SenderUserID > 0 && r.ensureBotAccess(ctx, botID, res.Message.SenderUserID) != nil {
			continue
		}
		message := botAPIMessageFromChannel(botID, res.Message)
		kind := domain.BotAPIUpdateEditedMessage
		view, err := r.deps.Channels.GetChannel(ctx, botID, res.Message.ChannelID)
		if err != nil || view.Forbidden || view.Self.Status != domain.ChannelMemberActive {
			continue
		}
		if view.Channel.Broadcast && !view.Channel.Megagroup {
			kind = domain.BotAPIUpdateKind("edited_channel_post")
		}
		r.enqueueBotAPIMessageSnapshot(ctx, botID, kind, message, []domain.Channel{view.Channel})
	}
}

// botAPIMemberChange 是一次群/频道成员状态变化，my_chat_member / chat_member 的入队单位。
type botAPIMemberChange struct {
	Channel                 domain.Channel
	ActorUserID             int64
	Previous                domain.ChannelMember
	Current                 domain.ChannelMember
	Date                    int
	Invite                  *domain.ChannelInvite
	ViaJoinRequest          bool
	ViaChatFolderInviteLink bool
}

// botAPIJoinedMemberChanges 把加入类结果（拉人、自行加入、邀请链接、批准申请）
// 转成成员变化。旧状态取 store 在同一事务内读出的 res.Previous（被踢后拉回是 kicked 等），
// 此前没有成员记录的按 left 计。
func botAPIJoinedMemberChanges(actorUserID int64, res domain.CreateChannelResult, date int) []botAPIMemberChange {
	out := make([]botAPIMemberChange, 0, len(res.Members))
	for _, member := range res.Members {
		if member.UserID == 0 || member.Status != domain.ChannelMemberActive {
			continue
		}
		out = append(out, botAPIMemberChange{
			Channel: res.Channel, ActorUserID: actorUserID, Date: date, Current: member,
			Previous: res.PreviousMember(member.ChannelID, member.UserID),
			Invite:   res.Joins[member.UserID].Invite, ViaJoinRequest: res.Joins[member.UserID].ViaJoinRequest,
		})
	}
	return out
}

// botAPIJoinRequestMemberChanges 是批准入群申请产生的加入；拒绝时结果里没有成员。
func botAPIJoinRequestMemberChanges(adminUserID int64, res domain.CreateChannelResult, date int) []botAPIMemberChange {
	return botAPIJoinedMemberChanges(adminUserID, res, date)
}

// botAPILeftMemberChanges 把退出结果转成成员变化：Members[0] 是退出者（已置 left），
// 群主退出时后面跟着接任的新群主。旧状态取 res.Previous（退出者与接任者变化前的真实记录）。
func botAPILeftMemberChanges(userID int64, res domain.CreateChannelResult, date int) []botAPIMemberChange {
	out := make([]botAPIMemberChange, 0, len(res.Members))
	for _, member := range res.Members {
		if member.UserID != userID && member.Role != domain.ChannelRoleCreator {
			continue
		}
		out = append(out, botAPIMemberChange{
			Channel: res.Channel, ActorUserID: userID, Date: date,
			Previous: res.PreviousMember(member.ChannelID, member.UserID), Current: member,
		})
	}
	return out
}

// enqueueBotAPIMemberUpdates 是所有成员变动路径的统一出口：当事人是机器人时给它入队
// my_chat_member；群里其他身为管理员的机器人入队 chat_member（是否订阅由 store 按
// allowed_updates 过滤，默认不投递）。走 botAPIEnqueueQueue，不拖慢发起变动的 RPC。
func (r *Router) enqueueBotAPIMemberUpdates(ctx context.Context, changes ...botAPIMemberChange) {
	if r == nil || r.deps.BotAPIUpdates == nil || len(changes) == 0 {
		return
	}
	r.botAPIEnqueueQueue.Enqueue(ctx, func(jobCtx context.Context) {
		admins := make(map[int64][]domain.ChannelMember)
		for _, change := range changes {
			r.enqueueBotAPIMemberChange(jobCtx, change, admins)
		}
	})
}

func (r *Router) enqueueBotAPIMemberChange(ctx context.Context, change botAPIMemberChange, adminsByChannel map[int64][]domain.ChannelMember) {
	subjectID := change.Current.UserID
	if subjectID == 0 {
		subjectID = change.Previous.UserID
	}
	if change.Channel.ID == 0 || subjectID == 0 {
		return
	}
	previous, current := change.Previous, change.Current
	previous.UserID, current.UserID = subjectID, subjectID
	if previous.Status == "" {
		// 之前不是成员（例如封禁一个从未入群的人）：按 left 计。
		previous.Role, previous.Status = domain.ChannelRoleMember, domain.ChannelMemberLeft
	}
	actorID := change.ActorUserID
	if actorID == 0 {
		actorID = subjectID
	}
	date := change.Date
	if date <= 0 {
		date = int(r.clock.Now().Unix())
	}
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: change.Channel.ID}
	payload := domain.BotChatMemberUpdated{
		Chat: chat, ActorUserID: actorID, Date: date,
		Old:                     domain.NewBotChatMemberSnapshot(previous),
		New:                     domain.NewBotChatMemberSnapshot(current),
		Invite:                  change.Invite,
		ViaJoinRequest:          change.ViaJoinRequest,
		ViaChatFolderInviteLink: change.ViaChatFolderInviteLink,
	}
	if payload.Invite != nil {
		invite := *payload.Invite
		invite.Link = publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+"+invite.Hash)
		payload.Invite = &invite
	}
	if payload.Old == payload.New {
		return
	}
	enqueue := func(botID int64, kind domain.BotAPIUpdateKind, viewer domain.ChannelMember) {
		item := payload
		if viewer.UserID == botID {
			item.Viewer = domain.NewBotChatMemberSnapshot(viewer)
		}
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: botID, Kind: kind, Peer: chat, Date: date, ChatMember: &item,
		}); err == nil && created {
			r.notifyBotAPIUpdate(botID)
		}
	}
	if isBot, err := r.botAPIKnownBot(ctx, subjectID); err == nil && isBot {
		// 当事人就是收件人：不能编辑自己，无需 Viewer。
		enqueue(subjectID, domain.BotAPIUpdateMyChatMember, domain.ChannelMember{})
	}
	admins, cached := adminsByChannel[change.Channel.ID]
	if !cached {
		admins = r.botAPIChannelAdminBots(ctx, change.Channel)
		adminsByChannel[change.Channel.ID] = admins
	}
	for _, admin := range admins {
		if admin.UserID != subjectID {
			enqueue(admin.UserID, domain.BotAPIUpdateChatMember, admin)
		}
	}
}

// botAPIChannelAdminBots 列出群里当前是管理员（含群主）的机器人及其成员记录
// （chat_member 载荷的 Viewer 取自这里）。
func (r *Router) botAPIChannelAdminBots(ctx context.Context, channel domain.Channel) []domain.ChannelMember {
	if r.deps.Channels == nil {
		return nil
	}
	// 候选列表要求查看者是成员；发起人可能刚离开/被踢，用群主视角更稳。
	viewerID := channel.CreatorUserID
	if viewerID == 0 {
		return nil
	}
	botIDs, err := r.botAPIChannelBotCandidates(ctx, viewerID, channel.ID)
	if err != nil {
		return nil
	}
	out := make([]domain.ChannelMember, 0, len(botIDs))
	for _, botID := range botIDs {
		member, err := r.deps.Channels.GetParticipant(ctx, botID, channel.ID, botID)
		if err != nil || member.Status != domain.ChannelMemberActive {
			continue
		}
		if member.Role == domain.ChannelRoleAdmin || member.Role == domain.ChannelRoleCreator {
			member.UserID = botID
			out = append(out, member)
		}
	}
	return out
}

func (r *Router) botAPIChannelBotCandidates(ctx context.Context, viewerUserID, channelID int64) ([]int64, error) {
	if r == nil || r.deps.Channels == nil || channelID == 0 {
		return nil, nil
	}
	provider, ok := r.deps.Channels.(botAPIChannelBotMemberProvider)
	if !ok {
		return nil, nil
	}
	return provider.ActiveBotMemberIDs(ctx, viewerUserID, channelID, domain.MaxSynchronousChannelDialogFanout)
}

func (r *Router) botAPIKnownBot(ctx context.Context, botID int64) (bool, error) {
	if botID == 0 {
		return false, nil
	}
	if r.deps.Bots != nil {
		if _, found, err := r.deps.Bots.BotInfo(ctx, botID); err != nil || found {
			return found, err
		}
	}
	return r.userIsBot(ctx, botID), nil
}

func botAPIMessageProjectable(msg domain.Message) bool {
	if msg.ID <= 0 {
		return false
	}
	if msg.Action != nil {
		switch msg.Action.Type {
		case domain.ChannelActionTopicCreate, domain.ChannelActionTopicEdit, domain.ChannelActionSuggestedPostApproval, domain.ChannelActionSuggestedPostSuccess, domain.ChannelActionSuggestedPostRefund:
			return true
		}
	}
	if msg.Body != "" {
		return true
	}
	return botAPIMessageMediaProjectable(msg.Media)
}

func botAPIMessageMediaProjectable(media *domain.MessageMedia) bool {
	if media.IsZero() {
		return false
	}
	switch media.Kind {
	case domain.MessageMediaKindGame:
		return media.Game != nil
	case domain.MessageMediaKindPaidMedia:
		return media.PaidMedia != nil
	case domain.MessageMediaKindDice:
		return media.Dice != nil
	case domain.MessageMediaKindPhoto:
		return media.Photo != nil
	case domain.MessageMediaKindDocument:
		return media.Document != nil
	case domain.MessageMediaKindContact:
		return media.Contact != nil
	case domain.MessageMediaKindGeo:
		return media.Geo != nil
	case domain.MessageMediaKindVenue:
		return media.Venue != nil
	case domain.MessageMediaKindPoll:
		return media.Poll != nil
	case domain.MessageMediaKindGeoLive:
		return media.GeoLive != nil
	case domain.MessageMediaKindTodo:
		return media.Todo != nil
	case domain.MessageMediaKindService:
		if media.ServiceAction == nil {
			return false
		}
		switch media.ServiceAction.Kind {
		case domain.MessageServiceActionPassportData:
			return media.ServiceAction.PassportData != nil && media.ServiceAction.PassportData.BotUserID > 0
		case domain.MessageServiceActionTopicCreate, domain.MessageServiceActionTopicEdit:
			return media.ServiceAction.ForumTopic != nil
		case domain.MessageServiceActionWebViewDataSent:
			return media.ServiceAction.WebViewData != nil
		case domain.MessageServiceActionRequestedPeer:
			return botAPIRequestedPeerProjectable(media.ServiceAction.RequestedPeer)
		case domain.MessageServiceActionPayment:
			// The service message has no media of its own; it is only reachable
			// as successful_payment, so it would otherwise be filtered out here
			// and the bot would never learn the charge id.
			return media.ServiceAction.Payment != nil && media.ServiceAction.Payment.ChargeID != ""
		default:
			return false
		}
	default:
		return false
	}
}

func botAPIRequestedPeerProjectable(action *domain.MessageRequestedPeerAction) bool {
	if action != nil && action.ManagedBot != nil {
		return action.ManagedBot.ID > 0 && action.ManagedBot.Bot
	}
	if action == nil || action.ButtonID == 0 || len(action.Peers) == 0 || len(action.Peers) > domain.MaxBotRequestedPeerQuantity {
		return false
	}
	details := make(map[domain.Peer]struct{}, len(action.Details))
	for _, detail := range action.Details {
		if detail.Peer.ID == 0 || (detail.Peer.Type != domain.PeerTypeUser && detail.Peer.Type != domain.PeerTypeChannel) {
			return false
		}
		details[detail.Peer] = struct{}{}
	}
	requiresDetails := action.NameRequested || action.UsernameRequested || action.PhotoRequested
	allUsers := true
	for _, peer := range action.Peers {
		if peer.ID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
			return false
		}
		if requiresDetails {
			if _, ok := details[peer]; !ok {
				return false
			}
		}
		allUsers = allUsers && peer.Type == domain.PeerTypeUser
	}
	return allUsers || (len(action.Peers) == 1 && action.Peers[0].Type == domain.PeerTypeChannel)
}

func (r *Router) enqueueBotAPIMessageSnapshot(ctx context.Context, botID int64, kind domain.BotAPIUpdateKind, message domain.Message, channels []domain.Channel) {
	events := r.enrichUpdateEvents(ctx, botID, []domain.UpdateEvent{{UserID: botID, Type: domain.UpdateEventNewMessage, Message: message, Channels: channels}})
	if len(events) != 1 {
		return
	}
	r.enqueueBotAPIObject(ctx, botID, kind, botapi.SnapshotMessage(message, events[0].Users, append(channels, events[0].Channels...)))
}

func (r *Router) botAPIObjectAuthorized(ctx context.Context, botID int64, item domain.BotAPIUpdate) bool {
	if item.Peer.Type == domain.PeerTypeChannel {
		switch item.Kind {
		case domain.BotAPIUpdateChatMember, domain.BotAPIUpdateMessageReaction, domain.BotAPIUpdateMessageReactionCount, domain.BotAPIUpdateChatJoinRequest:
			if r.deps.Channels == nil {
				return false
			}
			view, err := r.deps.Channels.GetChannel(ctx, botID, item.Peer.ID)
			if err != nil || view.Forbidden || view.Self.Status != domain.ChannelMemberActive {
				return false
			}
			admin := view.Self.Role == domain.ChannelRoleCreator || view.Self.Role == domain.ChannelRoleAdmin
			if !admin {
				return false
			}
			if item.Kind == domain.BotAPIUpdateChatJoinRequest && view.Self.Role != domain.ChannelRoleCreator && !view.Self.AdminRights.InviteUsers {
				return false
			}
		}
	}
	if item.Object == nil {
		return true
	}
	body, err := item.Object.Object()
	if err != nil {
		return false
	}
	switch item.Kind {
	case "message", "edited_message", "channel_post", "edited_channel_post", "guest_message", "inline_query", "chosen_inline_result":
		if from, ok := body["from"].(map[string]any); ok {
			if raw, ok := from["id"].(json.Number); ok {
				actorID, err := raw.Int64()
				if err != nil || r.ensureBotAccess(ctx, botID, actorID) != nil {
					return false
				}
			}
		}
	}
	switch item.Kind {
	case "chat_join_request":
		id, ok := body["query_id"].(string)
		if !ok || r.deps.Channels == nil {
			return false
		}
		queryID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return false
		}
		query, err := r.deps.Channels.GetChatJoinQuery(ctx, queryID)
		if err != nil || !query.CanResolve(botID, int(r.clock.Now().Unix()), "approve") {
			return false
		}
		guard, err := r.deps.Channels.GetChannelGuardBot(ctx, query.ChannelID)
		return err == nil && guard == botID
	case "business_message", "edited_business_message", "deleted_business_messages":
		connectionID, _ := body["business_connection_id"].(string)
		chat, _ := body["chat"].(map[string]any)
		id, ok := chat["id"].(json.Number)
		if !ok {
			return false
		}
		chatID, err := id.Int64()
		if err != nil {
			return false
		}
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: chatID}
		if chatID < -1000000000000 {
			peer = domain.Peer{Type: domain.PeerTypeChannel, ID: -chatID - 1000000000000}
		}
		_, err = r.botAPIBusinessPeer(ctx, botID, connectionID, "", peer)
		return err == nil
	case "message", "edited_message", "channel_post", "edited_channel_post", "chat_boost", "removed_chat_boost":
		chat, _ := body["chat"].(map[string]any)
		if chat["type"] == "private" {
			return true
		}
		id, ok := chat["id"].(json.Number)
		if !ok || r.deps.Channels == nil {
			return false
		}
		chatID, err := id.Int64()
		if err != nil || chatID >= -1000000000000 {
			return false
		}
		view, err := r.deps.Channels.GetChannel(ctx, botID, -chatID-1000000000000)
		if err != nil || view.Forbidden || view.Self.UserID != botID || view.Self.Status != domain.ChannelMemberActive {
			return false
		}
		if item.Kind == "chat_boost" || item.Kind == "removed_chat_boost" {
			return view.Self.Role == domain.ChannelRoleAdmin || view.Self.Role == domain.ChannelRoleCreator
		}
		if raw, ok := body["message_id"].(json.Number); ok {
			messageID, err := raw.Int64()
			if err != nil || messageID <= 0 || messageID > domain.MaxMessageBoxID {
				return false
			}
			history, err := r.deps.Channels.GetMessages(ctx, botID, view.Channel.ID, []int{int(messageID)})
			if err != nil {
				return false
			}
			for _, message := range history.Messages {
				if message.ID == int(messageID) && !message.Deleted {
					return true
				}
			}
			return false
		}
	}
	return true
}
