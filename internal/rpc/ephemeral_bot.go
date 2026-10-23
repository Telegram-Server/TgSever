package rpc

import (
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
