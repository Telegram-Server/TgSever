package rpc

import (
	"bytes"
	"context"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// botCallbackTimeout 是 getBotCallbackAnswer 的挂起上限：bot 未在窗口内
// setBotCallbackAnswer 即回 BOT_RESPONSE_TIMEOUT（不快速失败，给 bot 上线追答的窗口）。
const botCallbackTimeout = 25 * time.Second

func botResponseTimeoutErr() error { return tgerr.New(502, "BOT_RESPONSE_TIMEOUT") }
func dataInvalidErr() error        { return tgerr.New(400, "DATA_INVALID") }

type privateMessageByUIDService interface {
	GetMessageByUID(ctx context.Context, userID, uid int64) (domain.Message, bool, error)
}

// onMessagesGetBotCallbackAnswer 处理 inline callback 按钮点击：把同一 callback query
// 同时投递到在线 MTProto bot session 与 Bot API update_id 队列，挂起等待 bot 的
// setBotCallbackAnswer/answerCallbackQuery，或超时回 BOT_RESPONSE_TIMEOUT。
func (r *Router) onMessagesGetBotCallbackAnswer(ctx context.Context, req *tg.MessagesGetBotCallbackAnswerRequest) (*tg.MessagesBotCallbackAnswer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	data, hasData := req.GetData()
	if !hasData && !req.Game || req.Game && hasData {
		return nil, dataInvalidErr()
	}
	if len(data) > domain.MaxCallbackDataLen {
		return nil, dataInvalidErr()
	}
	// 校验目标消息存在于请求者自己的盒、且对端正是该 bot。
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	callback, err := r.resolveBotCallbackQuery(ctx, userID, peer, req.MsgID, data, req.Game)
	if err != nil {
		return nil, err
	}
	callback.ClientSession = clientSessionMetadataFromContext(ctx)
	botUserID := callback.BotUserID
	if err := r.ensureBotAccess(ctx, botUserID, userID); err != nil {
		return nil, err
	}

	// 内置（进程内）service bot 分支：@verifybot 这类 bot 没有 MTProto session、也没有
	// Bot API 消费者，走下面的「推 updateBotCallbackQuery + 挂起 25s」必然超时回
	// BOT_RESPONSE_TIMEOUT。因此在 registerContext / 推送之前同步问 responder：点击本身
	// 已由 resolveBotCallbackQuery 校验过（消息在请求者自己的盒里、对端正是该 bot、data
	// 确实出现在该消息的 inline keyboard 中），此处只是把答案交给拥有该 bot 的实现。
	// 不注册 query id：外部 setBotCallbackAnswer 因此无法伪造/覆盖内置 bot 的应答。
	if r.deps.ServiceBotCallbacks != nil && r.deps.ServiceBotCallbacks.HandlesBot(botUserID) {
		ans, handled, err := r.deps.ServiceBotCallbacks.OnCallbackQuery(ctx, callback)
		if err != nil {
			r.log.Warn("service bot callback query",
				zap.Int64("bot_user_id", botUserID), zap.Int64("user_id", userID), zap.Error(err))
			return nil, internalErr()
		}
		if !handled {
			return nil, dataInvalidErr()
		}
		return tgBotCallbackAnswer(ans), nil
	}

	queryID, pending, err := r.callbacks.registerContext(ctx, r.clock.Now(), botUserID, userID, botCallbackTimeout)
	if err != nil {
		r.log.Warn("register shared bot callback query", zap.Int64("bot_user_id", botUserID), zap.Error(err))
		return nil, internalErr()
	}
	defer r.callbacks.deregisterContext(context.Background(), botUserID, queryID)
	callback.ID = queryID
	if r.deps.Ephemeral != nil && peer.Type == domain.PeerTypeChannel {
		device, err := ephemeralDeviceFromContext(ctx, userID)
		if err == nil {
			now := r.clock.Now()
			created, err := r.deps.Ephemeral.PutCallbackAction(ctx, domain.EphemeralCallbackAction{
				QueryID: queryID, BotUserID: botUserID, UserID: userID, Peer: peer,
				MessageID: req.MsgID, TopMessageID: callback.TopMessageID, Device: device, CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralReplyWindow),
			})
			if err != nil || !created {
				return nil, internalErr()
			}
		}
	}

	// Bot API callback_query shares the dedicated durable update_id queue with message and
	// edited_message. The callback answer waiter itself remains ephemeral/process-local.
	if r.deps.BotAPIUpdates != nil {
		if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: botUserID,
			Kind:      domain.BotAPIUpdateCallbackQuery,
			Peer:      callback.Peer,
			MessageID: callback.MessageID,
			Date:      int(r.clock.Now().Unix()),
			Callback:  &callback,
		}); err != nil {
			r.log.Warn("enqueue bot api callback query",
				zap.Int64("bot_user_id", botUserID), zap.Int64("query_id", queryID), zap.Error(err))
			return nil, internalErr()
		} else if created {
			r.notifyBotAPIUpdate(botUserID)
		}
	}

	// updateBotCallbackQuery 是 ephemeral（无 pts/qts，不进 getDifference）；私聊 MessageID
	// 已翻译为 bot 视角 box id，channel 使用共享 message id。
	var update tg.UpdateClass
	if callback.InlineMessage != nil {
		inline := &tg.UpdateInlineBotCallbackQuery{
			QueryID: queryID, UserID: userID,
			MsgID: tgInputBotInlineMessageID(*callback.InlineMessage), ChatInstance: callback.ChatInstance,
		}
		if req.Game {
			inline.SetGameShortName(callback.GameShortName)
		} else {
			inline.SetData(data)
		}
		update = inline
	} else {
		direct := &tg.UpdateBotCallbackQuery{
			QueryID: queryID, UserID: userID, Peer: tgPeer(callback.Peer),
			MsgID: callback.MessageID, ChatInstance: callback.ChatInstance,
		}
		if req.Game {
			direct.SetGameShortName(callback.GameShortName)
		} else {
			direct.SetData(data)
		}
		update = direct
	}
	r.pushUserMessage(ctx, botUserID, "push bot callback query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})

	return r.waitBotCallbackAnswer(ctx, botUserID, queryID, pending)
}

func (r *Router) waitBotCallbackAnswer(ctx context.Context, botUserID, queryID int64, pending *pendingCallback) (*tg.MessagesBotCallbackAnswer, error) {
	waitCtx, cancel := context.WithTimeout(ctx, botCallbackTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ans := <-pending.ch:
			return tgBotCallbackAnswer(ans), nil
		case <-ticker.C:
			ans, found, err := r.callbacks.sharedAnswer(waitCtx, botUserID, queryID)
			if err != nil {
				r.log.Warn("read shared bot callback answer", zap.Int64("bot_user_id", botUserID), zap.Int64("query_id", queryID), zap.Error(err))
				continue
			}
			if found {
				return tgBotCallbackAnswer(ans), nil
			}
		case <-waitCtx.Done():
			return nil, botResponseTimeoutErr()
		}
	}
}

// resolveBotCallbackQuery validates the actual clicked button and binds it to its
// authoring bot, including signed via-bot inline identities.
func (r *Router) resolveBotCallbackQuery(ctx context.Context, userID int64, peer domain.Peer, msgID int, data []byte, game bool) (domain.BotCallbackQuery, error) {
	if peer.Type == domain.PeerTypeUser {
		msg, found, err := r.lookupOwnerMessage(ctx, userID, msgID)
		if err != nil {
			return domain.BotCallbackQuery{}, internalErr()
		}
		if !found || msg.Peer != peer || msg.ReplyMarkup == nil || msg.ReplyMarkup.Kind() != domain.MessageReplyMarkupInline || msg.ReplyMarkup.IsZero() {
			return domain.BotCallbackQuery{}, messageIDInvalidErr()
		}
		if !replyMarkupContainsCallback(msg.ReplyMarkup, data, game) || game && (msg.Media == nil || msg.Media.Game == nil) {
			return domain.BotCallbackQuery{}, dataInvalidErr()
		}
		if game && msg.BusinessBotID != 0 {
			botID := msg.BusinessBotID
			if msg.Media.Game.BotUserID != botID {
				return domain.BotCallbackQuery{}, dataInvalidErr()
			}
			owner, err := r.botAPIBusinessPeer(ctx, botID, msg.BusinessConnectionID, "can_reply", domain.Peer{Type: domain.PeerTypeUser, ID: userID})
			if err != nil || owner != msg.From.ID {
				return domain.BotCallbackQuery{}, dataInvalidErr()
			}
			provider, ok := r.deps.Messages.(privateMessageByUIDService)
			if !ok {
				return domain.BotCallbackQuery{}, internalErr()
			}
			sent, found, err := provider.GetMessageByUID(ctx, owner, msg.UID)
			if err != nil || !found {
				return domain.BotCallbackQuery{}, messageIDInvalidErr()
			}
			return domain.BotCallbackQuery{BotUserID: botID, UserID: userID, Peer: sent.Peer, MessageID: sent.ID, ChatInstance: chatInstanceFor(botID, userID), GameShortName: msg.Media.Game.ShortName}, nil
		}
		if msg.ViaBotID != 0 {
			if !r.userIsBot(ctx, msg.ViaBotID) {
				return domain.BotCallbackQuery{}, dataInvalidErr()
			}
			inlineID, ok := r.inputInlineMessageIDForPrivateMessage(msg.ViaBotID, msg).(*tg.InputBotInlineMessageID64)
			if !ok {
				return domain.BotCallbackQuery{}, messageIDInvalidErr()
			}
			return domain.BotCallbackQuery{
				BotUserID: msg.ViaBotID, UserID: userID,
				GameShortName: callbackGameName(msg.Media, game),
				ChatInstance:  chatInstanceFor(msg.ViaBotID, userID), Data: append([]byte(nil), data...),
				InlineMessage: domainInlineMessageID(inlineID),
			}, nil
		}
		if msg.From.Type != domain.PeerTypeUser || msg.From.ID == 0 || !r.userIsBot(ctx, msg.From.ID) {
			return domain.BotCallbackQuery{}, dataInvalidErr()
		}
		provider, ok := r.deps.Messages.(privateMessageByUIDService)
		if !ok || msg.UID == 0 {
			return domain.BotCallbackQuery{}, internalErr()
		}
		botMessage, found, err := provider.GetMessageByUID(ctx, msg.From.ID, msg.UID)
		if err != nil {
			return domain.BotCallbackQuery{}, internalErr()
		}
		if !found || botMessage.ID <= 0 || botMessage.OwnerUserID != msg.From.ID ||
			botMessage.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
			return domain.BotCallbackQuery{}, messageIDInvalidErr()
		}
		return domain.BotCallbackQuery{
			GameShortName: callbackGameName(msg.Media, game),
			BotUserID:     msg.From.ID,
			UserID:        userID,
			Peer:          botMessage.Peer,
			MessageID:     botMessage.ID,
			ChatInstance:  chatInstanceFor(msg.From.ID, userID),
			Data:          append([]byte(nil), data...),
		}, nil
	}
	if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return domain.BotCallbackQuery{}, peerIDInvalidErr()
	}
	history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
	if err != nil {
		return domain.BotCallbackQuery{}, channelInvalidErr(err)
	}
	if len(history.Messages) != 1 {
		return domain.BotCallbackQuery{}, messageIDInvalidErr()
	}
	msg := history.Messages[0]
	if msg.ID != msgID || msg.Deleted || msg.ReplyMarkup == nil || msg.ReplyMarkup.Kind() != domain.MessageReplyMarkupInline || msg.ReplyMarkup.IsZero() {
		return domain.BotCallbackQuery{}, messageIDInvalidErr()
	}
	if !replyMarkupContainsCallback(msg.ReplyMarkup, data, game) || game && (msg.Media == nil || msg.Media.Game == nil) {
		return domain.BotCallbackQuery{}, dataInvalidErr()
	}
	if msg.ViaBotID != 0 {
		if !r.userIsBot(ctx, msg.ViaBotID) {
			return domain.BotCallbackQuery{}, dataInvalidErr()
		}
		inlineID, ok := r.inputInlineMessageIDForChannelMessage(msg.ViaBotID, msg).(*tg.InputBotInlineMessageID64)
		if !ok {
			return domain.BotCallbackQuery{}, messageIDInvalidErr()
		}
		return domain.BotCallbackQuery{
			GameShortName: callbackGameName(msg.Media, game),
			BotUserID:     msg.ViaBotID, UserID: userID,
			ChatInstance: chatInstanceForPeer(msg.ViaBotID, peer), Data: append([]byte(nil), data...),
			InlineMessage: domainInlineMessageID(inlineID),
		}, nil
	}
	if msg.SenderUserID == 0 || !r.userIsBot(ctx, msg.SenderUserID) {
		return domain.BotCallbackQuery{}, dataInvalidErr()
	}
	topMessageID := 0
	if msg.ReplyTo != nil && msg.ReplyTo.ForumTopic {
		topMessageID = msg.ReplyTo.TopMessageID
	}
	return domain.BotCallbackQuery{
		GameShortName: callbackGameName(msg.Media, game),
		BotUserID:     msg.SenderUserID,
		UserID:        userID,
		Peer:          peer,
		MessageID:     msg.ID,
		TopMessageID:  topMessageID,
		ChatInstance:  chatInstanceForPeer(msg.SenderUserID, peer),
		Data:          append([]byte(nil), data...),
	}, nil
}

func domainInlineMessageID(id *tg.InputBotInlineMessageID64) *domain.BotInlineMessageID {
	if id == nil {
		return nil
	}
	return &domain.BotInlineMessageID{DCID: id.DCID, OwnerID: id.OwnerID, ID: id.ID, AccessHash: id.AccessHash}
}

func tgInputBotInlineMessageID(id domain.BotInlineMessageID) tg.InputBotInlineMessageIDClass {
	return &tg.InputBotInlineMessageID64{DCID: id.DCID, OwnerID: id.OwnerID, ID: id.ID, AccessHash: id.AccessHash}
}

func replyMarkupContainsCallback(markup *domain.MessageReplyMarkup, data []byte, game bool) bool {
	if markup == nil || markup.Kind() != domain.MessageReplyMarkupInline {
		return false
	}
	for _, row := range markup.Inline {
		for _, button := range row {
			if game && button.Type == domain.MarkupButtonGame || !game && button.Type == domain.MarkupButtonCallback && bytes.Equal(button.Data, data) {
				return true
			}
		}
	}
	return false
}

func callbackGameName(media *domain.MessageMedia, game bool) string {
	if game && media != nil && media.Game != nil {
		return media.Game.ShortName
	}
	return ""
}

// onMessagesSetBotCallbackAnswer 是 bot 对一次 callback query 的应答：解挂等待中的
// getBotCallbackAnswer。仅属主 bot 可解挂（callerBotID==pending.botUserID，I6）。
func (r *Router) onMessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	ans := domain.BotCallbackAnswer{Alert: req.Alert, CacheTime: req.CacheTime}
	if msg, ok := req.GetMessage(); ok {
		if utf8.RuneCountInString(msg) > domain.MaxBotCallbackAnswerLen {
			return false, messageTooLongErr()
		}
		ans.Message = msg
	}
	if url, ok := req.GetURL(); ok {
		ans.URL = url
	}
	// resolve 返回是否投递成功；未注册/超时/非属主一律 false。对 bot 而言答案是否
	// 被等待者接收无关紧要（官方恒返回 true），但非属主必须拒绝投递（防钓鱼弹窗）。
	if _, err := r.callbacks.resolveContext(ctx, botID, req.QueryID, ans); err != nil {
		return false, internalErr()
	}
	return true, nil
}

func tgBotCallbackAnswer(ans domain.BotCallbackAnswer) *tg.MessagesBotCallbackAnswer {
	out := &tg.MessagesBotCallbackAnswer{Alert: ans.Alert, CacheTime: ans.CacheTime}
	if ans.Message != "" {
		out.SetMessage(ans.Message)
	}
	if ans.URL != "" {
		out.SetURL(ans.URL)
		out.HasURL = true
	}
	return out
}
