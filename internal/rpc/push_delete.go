package rpc

import (
	"telesrv/internal/app/push"
	"telesrv/internal/domain"
)

// PushDeleteNotifier 是私聊删除同步推送的能力（*push.Service 满足它）。方法只入队、立即返回。
type PushDeleteNotifier interface {
	DeleteMessages(d push.DeletedMessages) bool
}

type pushDeleteNotifierHolder struct{ notifier PushDeleteNotifier }

// SetPushDeleteNotifier 注入删除同步推送服务；nil 表示不推送。推送服务依赖 router 的 presence，
// 因此在 router 创建之后注入。
func (r *Router) SetPushDeleteNotifier(notifier PushDeleteNotifier) {
	if notifier == nil {
		r.pushDeletes.Store(nil)
		return
	}
	r.pushDeletes.Store(&pushDeleteNotifierHolder{notifier: notifier})
}

// notifyPushDeletes 把一次私聊删除按 owner、会话交给推送服务，清掉 iOS 通知栏里对应的通知。
//
// delete_messages 事件不带会话（一次删除可能跨会话），所以不在 outbox 投递处触发，而是在
// 删除返回时用 store 按会话分好的 id（DeletedMessagesForUser.ByPeer）。双向删除时对方的
// 副本也在结果里，对方的设备同样会收到。收藏夹（会话是自己）没有通知，不推送。
func (r *Router) notifyPushDeletes(res domain.DeleteMessagesResult) {
	holder := r.pushDeletes.Load()
	if holder == nil {
		return
	}
	for _, deleted := range res.Deleted {
		for _, group := range deleted.ByPeer {
			if deleted.UserID == 0 || group.Peer.Type != domain.PeerTypeUser || group.Peer.ID == deleted.UserID {
				continue
			}
			holder.notifier.DeleteMessages(push.DeletedMessages{
				UserID:     deleted.UserID,
				Chat:       push.Chat{Kind: push.ChatPrivate, ID: group.Peer.ID},
				MessageIDs: group.MessageIDs,
			})
		}
	}
}
