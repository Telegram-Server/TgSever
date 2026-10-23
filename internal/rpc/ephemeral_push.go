package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/store"
)

const ephemeralPushSubscribeRetry = time.Second

func (r *Router) RunEphemeralPushSubscriber(ctx context.Context) {
	if r == nil || r.deps.EphemeralPush == nil {
		return
	}
	for {
		err := r.deps.EphemeralPush.SubscribeEphemeralPushes(ctx, func(ctx context.Context, event store.EphemeralPush) {
			if event.SourceID == "" || event.SourceID == r.instanceID {
				return
			}
			r.deliverEphemeralPushLocal(ctx, event)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.log.Warn("ephemeral push subscriber stopped", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(ephemeralPushSubscribeRetry):
		}
	}
}

func (r *Router) publishEphemeralPush(ctx context.Context, event store.EphemeralPush) {
	if r == nil || event.TargetUserID <= 0 {
		return
	}
	event.SourceID = r.instanceID
	if event.Date <= 0 {
		event.Date = int(r.clock.Now().Unix())
	}
	r.deliverEphemeralPushLocal(ctx, event)
	if r.deps.EphemeralPush != nil {
		if err := r.deps.EphemeralPush.PublishEphemeralPush(ctx, event); err != nil {
			r.log.Debug("publish ephemeral push", zap.String("kind", string(event.Kind)), zap.Int64("target_user_id", event.TargetUserID), zap.Error(err))
		}
	}
}

func (r *Router) deliverEphemeralPushLocal(ctx context.Context, event store.EphemeralPush) {
	if r == nil || r.deps.Sessions == nil || event.TargetUserID <= 0 || event.Message.ID <= 0 {
		return
	}
	if online, ok := r.deps.Sessions.(OnlineUserProvider); ok && !online.IsUserOnline(event.TargetUserID) {
		return
	}
	binder, ok := r.deps.Sessions.(SemanticTransientSessionBinder)
	if !ok {
		return
	}
	var updates tg.UpdatesClass
	var semantic tlprofile.SemanticID
	switch event.Kind {
	case store.EphemeralPushNew, store.EphemeralPushEdit:
		if event.TargetUserID != event.Message.ReceiverUserID || event.Message.Deleted {
			return
		}
		built, err := r.ephemeralMessageUpdates(ctx, event.TargetUserID, event.Message, event.Kind == store.EphemeralPushEdit)
		if err != nil {
			return
		}
		updates = built
		semantic = tlprofile.SemanticTypeUpdateNewEphemeralMessage
		if event.Kind == store.EphemeralPushEdit {
			semantic = tlprofile.SemanticTypeUpdateEditEphemeralMessage
		}
	case store.EphemeralPushDelete:
		if !event.Message.Deleted || (event.TargetUserID != event.Message.SenderUserID && event.TargetUserID != event.Message.ReceiverUserID) {
			return
		}
		updates = ephemeralDeleteUpdates(event.Message, event.Date)
		semantic = tlprofile.SemanticTypeUpdateDeleteEphemeralMessages
	case store.EphemeralPushCallback:
		callback := event.Callback
		if callback == nil || callback.BotUserID != event.TargetUserID || callback.MessageID != event.Message.ID || callback.Peer != event.Message.Peer {
			return
		}
		// msg_id 是临时消息 id，不在频道消息序列里；官方用 updateEphemeralBotCallbackQuery
		// 携带整条临时消息，TDLib 据此构造 callback query，不能复用 updateBotCallbackQuery。
		update := &tg.UpdateEphemeralBotCallbackQuery{
			QueryID: callback.ID, UserID: callback.UserID, MsgID: callback.MessageID,
			Data:    append([]byte(nil), callback.Data...),
			Message: tgEphemeralMessage(event.TargetUserID, event.Message),
		}
		update.SetPeer(tgPeer(callback.Peer))
		update.SetChatInstance(callback.ChatInstance)
		// 复用临时消息的 users/chats 投影：消息两端就是 bot 与点按钮的用户。
		built, err := r.ephemeralMessageUpdates(ctx, event.TargetUserID, event.Message, false)
		if err != nil {
			return
		}
		built.Updates = []tg.UpdateClass{update}
		built.Date = event.Date
		updates = built
		semantic = tlprofile.SemanticTypeUpdateEphemeralBotCallbackQuery
	default:
		return
	}
	if event.TargetBusinessAuthKey != ([8]byte{}) {
		_, _ = binder.PushToUserAuthKeyTransientCompatible(ctx, event.TargetUserID, event.TargetBusinessAuthKey, semantic, proto.MessageFromServer, updates, r.cfg.OutboundPushTimeout)
		return
	}
	_, _ = binder.PushToUserTransientCompatible(ctx, event.TargetUserID, semantic, proto.MessageFromServer, updates, r.cfg.OutboundPushTimeout)
}
