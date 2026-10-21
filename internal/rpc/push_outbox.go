package rpc

import (
	"context"

	"telesrv/internal/app/push"
	"telesrv/internal/domain"
)

// PushNotifier 是 rpc 层触发离线推送用到的推送服务能力（*push.Service 满足它）。
// 方法只入队、立即返回，不阻塞消息投递。
type PushNotifier interface {
	NewMessage(msg push.NewMessage) bool
	ReadHistory(read push.ReadHistory) bool
}

// PushOutboxObserver 把 outbox 投递转成离线推送：目标用户收到一条别人发来的私聊新消息时，
// 交给推送服务按设备判断是否推送（前台判定、渲染、加密都在推送服务的 worker 里做）。
//
// 只处理收件方视角的私聊消息：自己发出的（Out，含自己其他设备的同步）、已删除的、
// 服务消息和收藏夹（self chat）都不推送。群组与频道不走 outbox，由 #98 在频道扇出处接入；
// 已读同步（#100）也在这里：目标用户在别的设备上读过的私聊/频道，见 notifyPushReadHistory。
func PushOutboxObserver(notifier PushNotifier) OutboxDeliveryObserver {
	if notifier == nil {
		return nil
	}
	return func(_ context.Context, delivery OutboxDelivery) {
		event := delivery.Event
		target := delivery.Item.TargetUserID
		if target == 0 {
			target = event.UserID
		}
		if event.Type == domain.UpdateEventReadHistoryInbox {
			notifyPushReadHistory(notifier, target, event)
			return
		}
		if event.Type != domain.UpdateEventNewMessage {
			return
		}
		msg := event.Message
		if msg.Out || msg.Deleted || msg.ID == 0 ||
			msg.Peer.Type != domain.PeerTypeUser || msg.From.Type != domain.PeerTypeUser ||
			msg.From.ID == 0 || msg.From.ID == target || msg.Peer.ID == target {
			return
		}
		if msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindService {
			return
		}
		notifier.NewMessage(push.NewMessage{
			Chat:       push.Chat{Kind: push.ChatPrivate, ID: msg.Peer.ID},
			SenderID:   msg.From.ID,
			Recipients: []int64{target},
			MessageID:  msg.ID,
			Text:       msg.Body,
			Media:      msg.Media,
			Silent:     msg.Silent,
		})
	}
}

// notifyPushReadHistory 把“目标用户读到了 MaxID”（read_history_inbox：私聊来自 messages.readHistory，
// 频道来自 channels.readHistory 记下的用户事件）转成 READ_HISTORY 同步推送。
// 发给哪些设备（不在前台的）由推送服务判断，不受会话静音影响。收藏夹没有通知，跳过。
func notifyPushReadHistory(notifier PushNotifier, target int64, event domain.UpdateEvent) {
	if target == 0 || event.MaxID <= 0 || event.Peer.ID == 0 {
		return
	}
	var chat push.Chat
	switch event.Peer.Type {
	case domain.PeerTypeUser:
		if event.Peer.ID == target {
			return
		}
		chat = push.Chat{Kind: push.ChatPrivate, ID: event.Peer.ID}
	case domain.PeerTypeChannel:
		// 同步推送只用 channel_id，超级群与广播频道无需区分。
		chat = push.Chat{Kind: push.ChatChannel, ID: event.Peer.ID}
	default:
		return
	}
	notifier.ReadHistory(push.ReadHistory{UserID: target, Chat: chat, MaxID: event.MaxID})
}
