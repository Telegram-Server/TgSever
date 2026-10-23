package rpc

import (
	"unicode/utf8"

	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// onEphemeralSendFromBot 是 bot 会话（TDLib）调用 ephemeral.sendMessage 的入口。
// 授权、回复窗口、callback 动作和设备定向全部由 Ephemeral.SendFromBotLazy 负责，
// 与 Bot API 网关共用同一业务入口；内容（含 InputMedia 解析）在授权通过后才构建。
func (r *Router) onEphemeralSendFromBot(ctx context.Context, botID int64, peer domain.Peer, request *tg.EphemeralSendMessageRequest) (tg.UpdatesClass, error) {
	// anchor（replace_callback_query_message）需要替换 callback 来源消息，业务层
	// 没有这个语义；拒绝而不是当作普通发送悄悄降级。
	if request.Anchor || request.RandomID == 0 {
		return nil, inputRequestInvalidErr()
	}
	receiver, found, err := r.userFromInput(ctx, botID, request.ReceiverID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	topMessageID, replyID, err := ephemeralReplyFromInput(request.ReplyTo)
	if err != nil {
		return nil, err
	}
	queryID, _ := request.GetQueryID()
	if queryID != 0 && replyID != 0 {
		return nil, inputRequestInvalidErr()
	}
	message, fresh, err := r.deps.Ephemeral.SendFromBotLazy(ctx, domain.SendBotEphemeralRequest{
		BotUserID: botID, ReceiverUserID: receiver.ID, Peer: peer, RandomID: request.RandomID,
		TopMessageID: topMessageID, ReplyToEphemeralID: replyID,
		ActionMessageID: replyID, CallbackQueryID: queryID,
	}, func(buildCtx context.Context) (domain.EphemeralContent, error) {
		content, err := r.domainEphemeralInputContent(buildCtx, botID, request, true)
		if err != nil {
			return domain.EphemeralContent{}, err
		}
		if err := r.validateReplyMarkupForPeer(buildCtx, botID, peer, content.ReplyMarkup); err != nil {
			return domain.EphemeralContent{}, err
		}
		return content, nil
	})
	if err != nil {
		return nil, ephemeralBotRPCError(err)
	}
	if fresh {
		r.publishBotEphemeralPush(ctx, store.EphemeralPushNew, message)
	}
	if message.Deleted {
		return ephemeralDeleteUpdates(message, int(r.clock.Now().Unix())), nil
	}
	return r.ephemeralMessageUpdates(ctx, botID, message, false)
}

// onEphemeralEditMessage 分发 ephemeral.editMessage：welcome 模板走原有处理器；
// bot 编辑自己发出的临时消息；普通账号的非 welcome 调用沿用原规则（由 welcome
// 处理器拒绝）。
func (r *Router) onEphemeralEditMessage(ctx context.Context, request *tg.EphemeralEditMessageRequest) (tg.UpdatesClass, error) {
	if request == nil {
		return nil, inputRequestInvalidErr()
	}
	if request.Welcome || r.deps.Ephemeral == nil {
		return r.onEphemeralEditWelcomeMessage(ctx, request)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID <= 0 {
		return nil, internalErr()
	}
	if !r.userIsBot(ctx, userID) {
		return r.onEphemeralEditWelcomeMessage(ctx, request)
	}
	return r.onEphemeralEditFromBot(ctx, userID, request)
}

// onEphemeralEditFromBot 复用网关的 Ephemeral.EditFieldsFromBotLazy：所有权校验
// （发送者和接收者都要匹配）在构建替换内容之前完成。
func (r *Router) onEphemeralEditFromBot(ctx context.Context, botID int64, request *tg.EphemeralEditMessageRequest) (tg.UpdatesClass, error) {
	if request.ID <= 0 || request.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if request.Peer == nil {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, botID, request.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID <= 0 {
		return nil, peerIDInvalidErr()
	}
	receiver, found, err := r.userFromInput(ctx, botID, request.ReceiverID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	// 与 sendMessage 一致：临时消息没有承载富文本的字段。
	if request.RichMessage != nil {
		return nil, inputConstructorInvalidErr()
	}
	message, err := r.deps.Ephemeral.EditFieldsFromBotLazy(ctx, botID, receiver.ID, peer, request.ID, domain.EphemeralEditAny,
		func(buildCtx context.Context) (domain.EditEphemeralFields, error) {
			return r.domainEphemeralEditFields(buildCtx, botID, peer, request)
		})
	if err != nil {
		return nil, ephemeralBotRPCError(err)
	}
	r.publishBotEphemeralPush(ctx, store.EphemeralPushEdit, message)
	return r.ephemeralMessageUpdates(ctx, botID, message, true)
}

func (r *Router) domainEphemeralEditFields(ctx context.Context, botID int64, peer domain.Peer, request *tg.EphemeralEditMessageRequest) (domain.EditEphemeralFields, error) {
	var fields domain.EditEphemeralFields
	if text, ok := request.GetMessage(); ok {
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) > domain.MaxMessageTextLength || len(request.Entities) > domain.MaxMessageEntityCount {
			return domain.EditEphemeralFields{}, messageTooLongErr()
		}
		entities := domainMessageEntitiesForViewer(botID, request.Entities)
		if len(entities) != len(request.Entities) || !validEphemeralEntityBounds(text, entities) {
			return domain.EditEphemeralFields{}, tgerr.New(400, "ENTITY_BOUNDS_INVALID")
		}
		fields.SetMessage, fields.Message, fields.Entities = true, text, entities
	}
	if input := request.Media; input != nil {
		media, err := r.resolveInputMedia(ctx, botID, input)
		if err != nil {
			return domain.EditEphemeralFields{}, err
		}
		if !ephemeralMediaAllowed(media) {
			return domain.EditEphemeralFields{}, mediaTypeInvalidErr()
		}
		fields.SetMedia, fields.Media = true, media
	}
	if input := request.ReplyMarkup; input != nil {
		markup, err := domainReplyMarkupForSender(input, true)
		if err != nil {
			return domain.EditEphemeralFields{}, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, peer, markup); err != nil {
			return domain.EditEphemeralFields{}, err
		}
		fields.SetReplyMarkup, fields.ReplyMarkup = true, markup
	}
	if !fields.SetMessage && !fields.SetMedia && !fields.SetReplyMarkup {
		// TDLib 的 editEphemeralMessageReplyMarkup 传空键盘时不带任何 flag：移除键盘。
		fields.SetReplyMarkup = true
	}
	return fields, nil
}

// publishBotEphemeralPush 把 bot 发出的临时消息变化推给接收者。消息是对某个设备上
// 动作的回应时（OriginDevice 记录了接收者的设备）只推那台设备，否则推给接收者全部会话。
func (r *Router) publishBotEphemeralPush(ctx context.Context, kind store.EphemeralPushKind, message domain.EphemeralMessage) {
	r.publishEphemeralPush(ctx, store.EphemeralPush{
		Kind: kind, TargetUserID: message.ReceiverUserID,
		TargetBusinessAuthKey: message.OriginDevice.BusinessAuthKeyID, Message: message,
	})
}

// ephemeralBotRPCError 保留内容构建阶段已经映射好的 RPC 错误，其余按业务错误映射。
func ephemeralBotRPCError(err error) error {
	if _, ok := tgerr.As(err); ok {
		return err
	}
	return ephemeralRPCError(err)
}
