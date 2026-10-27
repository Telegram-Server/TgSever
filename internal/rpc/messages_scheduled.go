package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesGetScheduledMessages(ctx context.Context, req *tg.MessagesGetScheduledMessagesRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateMessageIDVector(req.ID); err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if r.deps.Messages == nil || !ok {
		result := &tg.MessagesMessages{Chats: r.chatsForMessageUpdate(ctx, userID, domain.Message{Peer: peer})}
		r.applyPeerReadModelsToMessages(ctx, userID, result)
		return result, nil
	}
	list, err := scheduledSvc.GetScheduledMessages(ctx, userID, domain.ScheduledMessageFilter{
		OwnerUserID: userID,
		Peer:        peer,
		IDs:         append([]int(nil), req.ID...),
		Limit:       len(req.ID),
	})
	if err != nil {
		return nil, messageIDInvalidErr()
	}
	return r.tgScheduledMessages(ctx, userID, peer, list), nil
}

func (r *Router) onMessagesGetScheduledHistory(ctx context.Context, req *tg.MessagesGetScheduledHistoryRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if r.deps.Messages == nil || !ok {
		if req.Hash != 0 {
			return &tg.MessagesMessagesNotModified{Count: 0}, nil
		}
		result := &tg.MessagesMessages{Chats: r.chatsForMessageUpdate(ctx, userID, domain.Message{Peer: peer})}
		r.applyPeerReadModelsToMessages(ctx, userID, result)
		return result, nil
	}
	list, err := scheduledSvc.ListScheduledMessages(ctx, userID, domain.ScheduledMessageFilter{
		OwnerUserID: userID,
		Peer:        peer,
		Limit:       maxScheduledMessagePage,
		Hash:        req.Hash,
	})
	if err != nil {
		return nil, peerIDInvalidErr()
	}
	if req.Hash != 0 && list.Hash == req.Hash && len(list.Messages) == 0 {
		return &tg.MessagesMessagesNotModified{Count: 0}, nil
	}
	return r.tgScheduledMessages(ctx, userID, peer, list), nil
}

func (r *Router) onMessagesSendScheduledMessages(ctx context.Context, req *tg.MessagesSendScheduledMessagesRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateMessageIDVector(req.ID); err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	// 只拦"立即发送"这个请求；到点由 ScheduledDispatcher 派发的已排队消息不经过这里，不回溯。
	if err := r.checkSendPermission(ctx, userID, peer); err != nil {
		return nil, err
	}
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if len(req.ID) == 0 {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	if r.deps.Messages == nil || !ok {
		return nil, messageIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	claimed, err := scheduledSvc.ClaimScheduledMessages(ctx, userID, domain.ScheduledMessageClaim{
		OwnerUserID: userID,
		Peer:        peer,
		IDs:         append([]int(nil), req.ID...),
		Now:         now,
		LeaseUntil:  now + 60,
	})
	if err != nil {
		return nil, messageIDInvalidErr()
	}
	return r.sendClaimedScheduledMessages(ctx, userID, peer, claimed, now)
}

func (r *Router) onMessagesDeleteScheduledMessages(ctx context.Context, req *tg.MessagesDeleteScheduledMessagesRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateMessageIDVector(req.ID); err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if r.deps.Messages == nil || !ok {
		date := int(r.clock.Now().Unix())
		return tgDeleteScheduledUpdates(peer, append([]int(nil), req.ID...), nil, r.chatsForMessageUpdate(ctx, userID, domain.Message{Peer: peer}), date), nil
	}
	date := int(r.clock.Now().Unix())
	deleted, err := scheduledSvc.DeleteScheduledMessages(ctx, userID, domain.ScheduledMessageFilter{
		OwnerUserID: userID,
		Peer:        peer,
		IDs:         append([]int(nil), req.ID...),
	}, date)
	if err != nil {
		return nil, messageIDInvalidErr()
	}
	ids := make([]int, 0, len(deleted))
	for _, msg := range deleted {
		ids = append(ids, msg.ID)
	}
	if len(ids) == 0 {
		return tgEmptyUpdates(date), nil
	}
	updates := tgDeleteScheduledUpdates(peer, ids, nil, r.chatsForMessageUpdates(ctx, userID, scheduledMessagesAsDomainMessages(deleted, userID)), date)
	r.pushUserUpdates(ctx, userID, updates)
	return updates, nil
}

func tgDeleteScheduledUpdates(peer domain.Peer, ids []int, sentIDs []int, chats []tg.ChatClass, date int) *tg.Updates {
	update := &tg.UpdateDeleteScheduledMessages{
		Peer:     tgPeer(peer),
		Messages: append([]int(nil), ids...),
	}
	if len(sentIDs) > 0 {
		update.SetSentMessages(append([]int(nil), sentIDs...))
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   []tg.UserClass{},
		Chats:   chats,
		Date:    date,
		Seq:     0,
	}
}

func scheduleDateIsImmediate(scheduleDate, now int) bool {
	return scheduleDate <= 0 || scheduleDate <= now+20
}

func (r *Router) scheduleOutgoing(ctx context.Context, userID int64, peer domain.Peer, p outgoingSend, scheduleDate, repeatPeriod int) (tg.UpdatesClass, error) {
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if r.deps.Messages == nil || !ok {
		return nil, peerIDInvalidErr()
	}
	if repeatPeriod != 0 {
		return nil, scheduleDateInvalidErr()
	}
	if peer.Type == domain.PeerTypeUser && p.randomID == 0 {
		// 私聊发送要求非零 random_id（幂等键）。random_id==0 的定时私聊消息到点
		// 投递必然在 SendPrivateText 失败并陷入重试，故在排程入口即拒绝。
		return nil, randomIDEmptyErr()
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Users != nil && peer.ID != userID {
		if _, found, err := r.deps.Users.ByID(ctx, userID, peer.ID); err != nil {
			return nil, internalErr()
		} else if !found {
			return nil, peerIDInvalidErr()
		}
	}
	replyTo, err := r.messageReplyFromInput(ctx, userID, peer, p.replyToInput)
	if err != nil {
		return nil, err
	}
	sendAs, err := r.resolveSendAsPeer(ctx, userID, peer, p.sendAsInput)
	if err != nil {
		return nil, err
	}
	date := int(r.clock.Now().Unix())
	msg, err := scheduledSvc.ScheduleMessage(ctx, userID, domain.ScheduleMessageRequest{
		OwnerUserID:          userID,
		Peer:                 peer,
		RandomID:             p.randomID,
		Message:              p.message,
		Entities:             domainMessageEntitiesForViewer(userID, p.entities),
		Media:                p.media,
		RichMessage:          p.richMessage,
		Silent:               p.silent,
		NoForwards:           p.noforwards,
		ReplyTo:              replyTo,
		SendAs:               sendAs,
		GroupedID:            p.groupedID,
		ScheduleDate:         scheduleDate,
		ScheduleRepeatPeriod: repeatPeriod,
		Date:                 date,
	})
	if err != nil {
		return nil, messageSendErr(err)
	}
	if p.clearDraft {
		r.clearDraftAfterSend(ctx, userID, peer, replyTo)
	}
	updates := r.tgNewScheduledMessageUpdates(ctx, userID, msg, p.randomID, date)
	r.pushUserUpdates(ctx, userID, updates)
	return updates, nil
}

func (r *Router) sendClaimedScheduledMessages(ctx context.Context, userID int64, peer domain.Peer, claimed []domain.ScheduledMessage, date int) (tg.UpdatesClass, error) {
	if len(claimed) == 0 {
		return tgEmptyUpdates(date), nil
	}
	combined := &tg.Updates{
		Updates: make([]tg.UpdateClass, 0, len(claimed)*3),
		Users:   []tg.UserClass{},
		Chats:   r.chatsForMessageUpdates(ctx, userID, scheduledMessagesAsDomainMessages(claimed, userID)),
		Date:    date,
		Seq:     0,
	}
	deletedIDs := make([]int, 0, len(claimed))
	sentIDs := make([]int, 0, len(claimed))
	for _, scheduled := range claimed {
		updates, err := r.sendScheduledMessage(ctx, userID, scheduled)
		if err != nil {
			if scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService); ok {
				_ = scheduledSvc.ReleaseScheduledMessage(ctx, scheduled.OwnerUserID, scheduled.ID, err.Error())
			}
			return nil, err
		}
		sentID := sentMessageIDFromUpdates(updates)
		scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
		if !ok {
			return nil, internalErr()
		}
		if err := scheduledSvc.MarkScheduledMessageSent(ctx, scheduled.OwnerUserID, scheduled.ID, sentID, date); err != nil {
			return nil, internalErr()
		}
		deletedIDs = append(deletedIDs, scheduled.ID)
		sentIDs = append(sentIDs, sentID)
		if up, ok := updates.(*tg.Updates); ok {
			combined.Updates = append(combined.Updates, up.Updates...)
			combined.Users = mergeTGUsers(combined.Users, up.Users)
			combined.Chats = mergeTGChats(combined.Chats, up.Chats)
			if up.Date > combined.Date {
				combined.Date = up.Date
			}
		}
	}
	deleteUpdate := tgDeleteScheduledUpdates(peer, deletedIDs, sentIDs, r.chatsForMessageUpdates(ctx, userID, scheduledMessagesAsDomainMessages(claimed, userID)), date)
	combined.Updates = append(combined.Updates, deleteUpdate.Updates...)
	combined.Chats = mergeTGChats(combined.Chats, deleteUpdate.Chats)
	r.pushUserUpdates(ctx, userID, deleteUpdate)
	return combined, nil
}

// sendScheduledMessage 把一条已认领的定时消息按排程时持久化的内容发出：回复目标、
// send_as、相册分组都沿用定时行上保存的值。
func (r *Router) sendScheduledMessage(ctx context.Context, userID int64, scheduled domain.ScheduledMessage) (tg.UpdatesClass, error) {
	sendAs := scheduled.SendAs
	if sendAs != nil {
		// 与立即发送一致：发出时已无权以该身份发言即 SEND_AS_PEER_INVALID，不发出（行释放回
		// pending，权限恢复后可再发），绝不退化为以本人身份发出——那会暴露匿名管理员。
		checked, err := r.checkSendAsPeer(ctx, userID, scheduled.Peer, *sendAs)
		if err != nil {
			return nil, err
		}
		sendAs = checked
	}
	p := outgoingSend{
		randomID:    scheduled.RandomID,
		message:     scheduled.Message,
		entities:    tgInputMessageEntities(scheduled.Entities),
		media:       scheduled.Media,
		richMessage: scheduled.RichMessage,
		silent:      scheduled.Silent,
		noforwards:  scheduled.NoForwards,
		// 回复目标已在排程时解析校验并持久化，发出时直接沿用。
		replyTo:      scheduled.ReplyTo,
		replyToReady: true,
		sendAs:       sendAs,
		sendAsReady:  true,
		forward:      scheduled.Forward,
		// 定时转发的源帖子在这次发送的事务里计转发数。
		forwardedPost: scheduled.ForwardedPost,
		// 同组定时消息无论一次还是分批发出，都沿用排程时持久化的同一 grouped_id。
		groupedID: scheduled.GroupedID,
	}
	updates, _, err := r.sendOutgoing(ctx, userID, scheduled.Peer, p)
	if err != nil && p.replyTo != nil && tgerr.Is(err, "REPLY_MESSAGE_ID_INVALID") {
		// 回复目标在排程后被删除：按官方行为照常发出、不带回复引用，不能让这条定时消息
		// 因一个已不存在的引用永远卡在重试里。
		p.replyTo = nil
		updates, _, err = r.sendOutgoing(ctx, userID, scheduled.Peer, p)
	}
	return updates, err
}

func (r *Router) tgScheduledMessages(ctx context.Context, userID int64, peer domain.Peer, list domain.ScheduledMessageList) tg.MessagesMessagesClass {
	messages := scheduledMessagesAsDomainMessages(list.Messages, userID)
	out := make([]tg.MessageClass, 0, len(messages))
	for _, msg := range messages {
		item := tgMessage(msg)
		if scheduled, ok := item.(*tg.Message); ok {
			scheduled.SetFromScheduled(true)
		}
		if item != nil {
			out = append(out, item)
		}
	}
	chats := r.chatsForMessageUpdates(ctx, userID, messages)
	if len(chats) == 0 && peer.Type == domain.PeerTypeChannel {
		chats = r.chatsForMessageUpdate(ctx, userID, domain.Message{Peer: peer})
	}
	result := &tg.MessagesMessages{
		Messages: out,
		Chats:    chats,
		Users:    r.usersForMessageUpdates(ctx, userID, messages),
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	return result
}

func (r *Router) tgNewScheduledMessageUpdates(ctx context.Context, userID int64, msg domain.ScheduledMessage, randomID int64, date int) *tg.Updates {
	domainMsg := scheduledMessageAsDomainMessage(msg, userID)
	item := tgMessage(domainMsg)
	if scheduled, ok := item.(*tg.Message); ok {
		scheduled.SetFromScheduled(true)
	}
	updates := make([]tg.UpdateClass, 0, 2)
	if randomID != 0 {
		updates = append(updates, &tg.UpdateMessageID{ID: msg.ID, RandomID: randomID})
	}
	if item == nil {
		item = &tg.MessageEmpty{ID: msg.ID}
	}
	updates = append(updates, &tg.UpdateNewScheduledMessage{Message: item})
	return &tg.Updates{
		Updates: updates,
		Users:   r.usersForMessageUpdate(ctx, userID, domainMsg),
		Chats:   r.chatsForMessageUpdate(ctx, userID, domainMsg),
		Date:    date,
		Seq:     0,
	}
}

func scheduledMessagesAsDomainMessages(messages []domain.ScheduledMessage, viewerUserID int64) []domain.Message {
	out := make([]domain.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, scheduledMessageAsDomainMessage(msg, viewerUserID))
	}
	return out
}

func scheduledMessageAsDomainMessage(msg domain.ScheduledMessage, viewerUserID int64) domain.Message {
	from := domain.Peer{Type: domain.PeerTypeUser, ID: msg.OwnerUserID}
	if msg.SendAs != nil && msg.SendAs.ID != 0 {
		from = *msg.SendAs
	}
	return domain.Message{
		ID:          msg.ID,
		RandomID:    msg.RandomID,
		OwnerUserID: msg.OwnerUserID,
		Peer:        msg.Peer,
		From:        from,
		Date:        msg.ScheduleDate,
		Out:         msg.OwnerUserID == viewerUserID,
		Silent:      msg.Silent,
		NoForwards:  msg.NoForwards,
		Body:        msg.Message,
		Entities:    append([]domain.MessageEntity(nil), msg.Entities...),
		ReplyTo:     msg.ReplyTo,
		Forward:     msg.Forward,
		Media:       msg.Media,
		RichMessage: msg.RichMessage,
		GroupedID:   msg.GroupedID,
	}
}

func sentMessageIDFromUpdates(updates tg.UpdatesClass) int {
	up, ok := updates.(*tg.Updates)
	if !ok {
		return 0
	}
	for _, update := range up.Updates {
		switch v := update.(type) {
		case *tg.UpdateMessageID:
			return v.ID
		case *tg.UpdateNewMessage:
			if msg, ok := v.Message.(*tg.Message); ok {
				return msg.ID
			}
		case *tg.UpdateNewChannelMessage:
			if msg, ok := v.Message.(*tg.Message); ok {
				return msg.ID
			}
		}
	}
	return 0
}

func (r *Router) scheduleForwardMessages(ctx context.Context, userID int64, fromPeer, toPeer domain.Peer, req *tg.MessagesForwardMessagesRequest, replyTo *domain.MessageReply, sendAs *domain.Peer, preloadedSources []forwardSource) (tg.UpdatesClass, error) {
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if r.deps.Messages == nil || !ok {
		return nil, peerIDInvalidErr()
	}
	sources, err := r.forwardSourcesForRequest(ctx, userID, fromPeer, req.ID, preloadedSources)
	if err != nil {
		return nil, messageForwardErr(err)
	}
	date := int(r.clock.Now().Unix())
	updates := &tg.Updates{
		Updates: make([]tg.UpdateClass, 0, len(sources)*2),
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
	domainMessages := make([]domain.Message, 0, len(sources))
	// 定时转发不查重放，sources 与 req.ID 一一对应；同一源相册得到同一个新 grouped_id，
	// 持久化在定时行上，到点发出时原样沿用。
	groupedIDs := forwardGroupedIDs(userID, req.RandomID, sources, nil)
	// 源帖子引用持久化在定时行上，到点发出时在发送事务里计转发数；同一请求里同一帖子只有
	// 第一份带上引用。
	postForwards := forwardPostCounter{}
	for i, source := range sources {
		forward := source.forward
		if req.DropAuthor {
			forward = nil
		}
		if forward != nil && toPeer.Type == domain.PeerTypeUser && toPeer.ID == userID {
			// 与立即转发一致：转发进收藏夹记下来源会话与原消息，收藏夹据此按来源分组。
			saved := *forward
			saved.SavedFrom = fromPeer
			saved.SavedFromMsgID = req.ID[i]
			forward = &saved
		}
		msg, err := scheduledSvc.ScheduleMessage(ctx, userID, domain.ScheduleMessageRequest{
			OwnerUserID:          userID,
			Peer:                 toPeer,
			RandomID:             req.RandomID[i],
			Message:              source.body,
			Entities:             append([]domain.MessageEntity(nil), source.entities...),
			Media:                source.media,
			Silent:               req.Silent,
			NoForwards:           req.Noforwards,
			ReplyTo:              replyTo,
			Forward:              forward,
			SendAs:               sendAs,
			GroupedID:            groupedIDs[i],
			ForwardedPost:        postForwards.pending(source.post),
			ScheduleDate:         req.ScheduleDate,
			ScheduleRepeatPeriod: req.ScheduleRepeatPeriod,
			Date:                 date,
		})
		if err != nil {
			return nil, messageForwardErr(err)
		}
		postForwards.written(source.post, false)
		domainMsg := scheduledMessageAsDomainMessage(msg, userID)
		domainMessages = append(domainMessages, domainMsg)
		if req.RandomID[i] != 0 {
			updates.Updates = append(updates.Updates, &tg.UpdateMessageID{ID: msg.ID, RandomID: req.RandomID[i]})
		}
		item := tgMessage(domainMsg)
		if scheduled, ok := item.(*tg.Message); ok {
			scheduled.SetFromScheduled(true)
		}
		if item == nil {
			item = &tg.MessageEmpty{ID: msg.ID}
		}
		updates.Updates = append(updates.Updates, &tg.UpdateNewScheduledMessage{Message: item})
	}
	updates.Users = r.usersForMessageUpdates(ctx, userID, domainMessages)
	updates.Chats = r.chatsForMessageUpdates(ctx, userID, domainMessages)
	r.pushUserUpdates(ctx, userID, updates)
	return updates, nil
}

func (r *Router) editScheduledMessage(ctx context.Context, userID int64, peer domain.Peer, id int, message string, setMessage bool, entities []tg.MessageEntityClass, richMessage *domain.MessageRichMessage, setRichMessage bool, scheduleDate int) (tg.UpdatesClass, error) {
	if r.deps.Messages == nil {
		return nil, messageIDInvalidErr()
	}
	scheduledSvc, ok := r.deps.Messages.(scheduledMessagesService)
	if !ok {
		return nil, messageIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	if scheduleDateIsImmediate(scheduleDate, now) {
		return nil, scheduleDateInvalidErr()
	}
	msg, err := scheduledSvc.EditScheduledMessage(ctx, userID, domain.EditScheduledMessageRequest{
		OwnerUserID:    userID,
		Peer:           peer,
		ID:             id,
		SetMessage:     setMessage,
		Message:        message,
		Entities:       domainMessageEntitiesForViewer(userID, entities),
		SetRichMessage: setRichMessage,
		RichMessage:    richMessage,
		ScheduleDate:   scheduleDate,
		Date:           now,
	})
	if err != nil {
		return nil, messageEditErr(err)
	}
	updates := r.tgNewScheduledMessageUpdates(ctx, userID, msg, 0, now)
	r.pushUserUpdates(ctx, userID, updates)
	return updates, nil
}
