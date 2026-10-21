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
}

// PushOutboxObserver 把 outbox 投递转成离线推送：目标用户收到一条别人发来的私聊新消息时，
// 交给推送服务按设备判断是否推送（前台判定、渲染、加密都在推送服务的 worker 里做）。
//
// 只处理收件方视角的私聊消息：自己发出的（Out，含自己其他设备的同步）、已删除的、
// 服务消息和收藏夹（self chat）都不推送。群组与频道不走 outbox，由 #98 在频道扇出处接入；
// 已读/删除同步（#100）在这里按事件类型扩展。
func PushOutboxObserver(notifier PushNotifier) OutboxDeliveryObserver {
	if notifier == nil {
		return nil
	}
	return func(_ context.Context, delivery OutboxDelivery) {
		event := delivery.Event
		if event.Type != domain.UpdateEventNewMessage {
			return
		}
		target := delivery.Item.TargetUserID
		if target == 0 {
			target = event.UserID
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
