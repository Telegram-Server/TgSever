package rpc

import (
	"sync/atomic"

	"telesrv/internal/app/push"
	"telesrv/internal/domain"
)

// ChannelPushNotifier 是群组/频道离线推送用到的推送服务能力（*push.Service 满足它）。
// 方法只入队、立即返回：成员列表由推送服务的 worker 分批读取，不阻塞实时分发。
type ChannelPushNotifier interface {
	ChannelMessage(msg push.NewMessage) bool
}

type channelPushSlot struct {
	p atomic.Pointer[ChannelPushNotifier]
}

func (s *channelPushSlot) load() ChannelPushNotifier {
	if p := s.p.Load(); p != nil {
		return *p
	}
	return nil
}

// SetChannelPushNotifier 注入群组/频道离线推送入口；nil 关闭。推送服务依赖 router（presence），
// 所以在 router 创建之后注入。
func (r *Router) SetChannelPushNotifier(n ChannelPushNotifier) {
	if n == nil {
		r.channelPush.p.Store(nil)
		return
	}
	r.channelPush.p.Store(&n)
}

// notifyChannelMessagePush 把一条新的群组/频道消息交给推送服务。实时分发的 Recipients 只含在线成员，
// 这里不用它：推送服务按完整成员列表分批推送，连接还在、但 App 已报后台的成员也要推。
func (r *Router) notifyChannelMessagePush(originUserID int64, res domain.SendChannelMessageResult) {
	notifier := r.channelPush.load()
	if notifier == nil {
		return
	}
	if msg, ok := channelMessagePush(originUserID, res); ok {
		notifier.ChannelMessage(msg)
	}
}

// channelMessagePush 把频道消息结果转成推送事件；不该推送的返回 false：
// 重放（Duplicate）、服务消息、monoforum（频道私信，收件人不是成员列表）、
// 讨论组里自动转发的频道帖子（频道成员已因原帖收到推送）。
func channelMessagePush(originUserID int64, res domain.SendChannelMessageResult) (push.NewMessage, bool) {
	ch, m := res.Channel, res.Message
	if res.Duplicate || ch.ID == 0 || ch.Monoforum || m.ID == 0 || m.Action != nil {
		return push.NewMessage{}, false
	}
	if m.Media != nil && m.Media.Kind == domain.MessageMediaKindService {
		return push.NewMessage{}, false
	}
	if ch.LinkedChatID != 0 && m.From == (domain.Peer{Type: domain.PeerTypeChannel, ID: ch.LinkedChatID}) {
		return push.NewMessage{}, false
	}
	kind := push.ChatGroup
	if ch.Broadcast && !ch.Megagroup {
		kind = push.ChatChannel
	}
	sender := m.SenderUserID
	if sender == 0 {
		sender = originUserID
	}
	msg := push.NewMessage{
		Chat:      push.Chat{Kind: kind, ID: ch.ID, TopicID: forumTopicOf(m)},
		ChatTitle: ch.Title,
		SenderID:  sender,
		MessageID: m.ID,
		Text:      m.Body,
		Media:     m.Media,
		Silent:    m.Silent,
	}
	// 匿名管理员 / 以频道身份发言：不暴露真实发送者，文案里的发送者用群名。
	// （以另一个频道身份发言时本应展示那个频道的名字；这里不为推送额外查询，统一用群名。）
	if kind == push.ChatGroup && m.From.Type == domain.PeerTypeChannel {
		msg.AuthorName = ch.Title
	}
	return msg, true
}

// forumTopicOf 返回消息所在的 forum 话题（话题根消息 id）：直接回复话题根时 TopMessageID 为 0，
// 话题就是被回复的那条。不在话题里返回 0。
func forumTopicOf(m domain.ChannelMessage) int {
	if m.ReplyTo == nil || !m.ReplyTo.ForumTopic {
		return 0
	}
	if m.ReplyTo.TopMessageID > 0 {
		return m.ReplyTo.TopMessageID
	}
	return m.ReplyTo.MessageID
}
