package rpc

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// scheduledTestClock 是可前拨的路由时钟：派发器按它的 now 认领到期定时消息。
type scheduledTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *scheduledTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *scheduledTestClock) Timer(d time.Duration) clock.Timer   { return clock.System.Timer(d) }
func (c *scheduledTestClock) Ticker(d time.Duration) clock.Ticker { return clock.System.Ticker(d) }

func (c *scheduledTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// scheduledFixture 是真实内存用户/频道/消息服务：定时消息存进 memory.MessageStore 的定时
// 队列（与 postgres 同一契约），排程、到点派发都走生产代码路径，结果从真实历史里读回。
type scheduledFixture struct {
	t        *testing.T
	clock    *scheduledTestClock
	router   *Router
	messages *appmessages.Service
	channels *memory.ChannelStore
	owner    domain.User
	friend   domain.User
}

func newScheduledFixture(t *testing.T) scheduledFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 81, Phone: "15550019801", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 82, Phone: "15550019802", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	messages := appmessages.NewService(memory.NewMessageStore(dialogStore), dialogStore)
	channelStore := memory.NewChannelStore()
	clk := &scheduledTestClock{now: time.Now()}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Messages: messages,
		Files:    albumFakeFiles(),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clk)
	return scheduledFixture{t: t, clock: clk, router: r, messages: messages, channels: channelStore, owner: owner, friend: friend}
}

func (f scheduledFixture) ctx(user domain.User) context.Context {
	return WithUserID(context.Background(), user.ID)
}

func (f scheduledFixture) friendPeer() *tg.InputPeerUser {
	return &tg.InputPeerUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash}
}

// scheduleDate 是一小时后的排程时间。
func (f scheduledFixture) scheduleDate() int {
	return int(f.clock.Now().Unix()) + 3600
}

// dispatch 把时钟拨过所有排程时间，让后台派发器跑一轮；返回是否认领到消息。
func (f scheduledFixture) dispatch() bool {
	f.t.Helper()
	f.clock.advance(2 * time.Hour)
	return NewScheduledDispatcher(f.router, zaptest.NewLogger(f.t)).DispatchOnce(context.Background())
}

// historyByRandomID 从 owner 与 peer 的会话历史里找 random_id 对应的消息。
func (f scheduledFixture) historyByRandomID(peer domain.Peer, randomID int64) (domain.Message, bool) {
	f.t.Helper()
	history, err := f.messages.GetHistory(context.Background(), f.owner.ID, domain.MessageFilter{HasPeer: true, Peer: peer, Limit: 50})
	if err != nil {
		f.t.Fatalf("history with %+v: %v", peer, err)
	}
	for _, msg := range history.Messages {
		if msg.RandomID == randomID {
			return msg, true
		}
	}
	return domain.Message{}, false
}

func (f scheduledFixture) privateByRandomID(randomID int64) (domain.Message, bool) {
	return f.historyByRandomID(domain.Peer{Type: domain.PeerTypeUser, ID: f.friend.ID}, randomID)
}

func (f scheduledFixture) sendPrivate(randomID int64, text string) domain.Message {
	f.t.Helper()
	if _, err := f.router.onMessagesSendMessage(f.ctx(f.owner), &tg.MessagesSendMessageRequest{
		Peer:     f.friendPeer(),
		Message:  text,
		RandomID: randomID,
	}); err != nil {
		f.t.Fatalf("send %q: %v", text, err)
	}
	msg, ok := f.privateByRandomID(randomID)
	if !ok {
		f.t.Fatalf("sent message random_id=%d not in history", randomID)
	}
	return msg
}

// createGroup 由 owner 建一个含 friend 的超级群。
func (f scheduledFixture) createGroup(title string) *tg.Channel {
	f.t.Helper()
	created, err := f.router.onMessagesCreateChat(f.ctx(f.owner), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash}},
		Title: title,
	})
	if err != nil {
		f.t.Fatalf("create group: %v", err)
	}
	return created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
}

// channelByRandomID 从频道历史里找 random_id 对应的消息。
func (f scheduledFixture) channelByRandomID(channel *tg.Channel, randomID int64) (domain.ChannelMessage, bool) {
	f.t.Helper()
	history, err := f.channels.ListChannelHistory(context.Background(), f.owner.ID, domain.ChannelHistoryFilter{ChannelID: channel.ID, Limit: 50})
	if err != nil {
		f.t.Fatalf("channel history: %v", err)
	}
	for _, msg := range history.Messages {
		if msg.RandomID == randomID {
			return msg, true
		}
	}
	return domain.ChannelMessage{}, false
}

// scheduledHistory 是 user 在 peer 下经 messages.getScheduledHistory 看到的定时队列。
func (f scheduledFixture) scheduledHistory(user domain.User, peer tg.InputPeerClass) []*tg.Message {
	f.t.Helper()
	res, err := f.router.onMessagesGetScheduledHistory(f.ctx(user), &tg.MessagesGetScheduledHistoryRequest{Peer: peer})
	if err != nil {
		f.t.Fatalf("get scheduled history: %v", err)
	}
	out := make([]*tg.Message, 0)
	for _, item := range res.(*tg.MessagesMessages).Messages {
		msg, ok := item.(*tg.Message)
		if !ok {
			f.t.Fatalf("scheduled history item %T, want *tg.Message", item)
		}
		out = append(out, msg)
	}
	return out
}

func (f scheduledFixture) setFriendAdminRights(group *tg.Channel, rights tg.ChatAdminRights) {
	f.t.Helper()
	if _, err := f.router.onChannelsEditAdmin(f.ctx(f.owner), &tg.ChannelsEditAdminRequest{
		Channel:     inputChannel(group),
		UserID:      &tg.InputUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash},
		AdminRights: rights,
	}); err != nil {
		f.t.Fatalf("edit friend admin rights: %v", err)
	}
}

func (f scheduledFixture) sendInChannel(user domain.User, channel *tg.Channel, randomID int64, sendAs tg.InputPeerClass, scheduleDate int) {
	f.t.Helper()
	req := &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(channel), Message: "in group", RandomID: randomID}
	if sendAs != nil {
		req.SetSendAs(sendAs)
	}
	if scheduleDate != 0 {
		req.SetScheduleDate(scheduleDate)
	}
	if _, err := f.router.onMessagesSendMessage(f.ctx(user), req); err != nil {
		f.t.Fatalf("send in channel (schedule=%d): %v", scheduleDate, err)
	}
}

func (f scheduledFixture) forward(req *tg.MessagesForwardMessagesRequest, scheduleDate int) {
	f.t.Helper()
	if scheduleDate != 0 {
		req.SetScheduleDate(scheduleDate)
	}
	if _, err := f.router.onMessagesForwardMessages(f.ctx(f.owner), req); err != nil {
		f.t.Fatalf("forward (schedule=%d): %v", scheduleDate, err)
	}
}

// sentView 是一条已发出消息里定时排程必须原样保住的部分。
type sentView struct {
	From    domain.Peer
	SendAs  *domain.Peer
	Body    string
	ReplyTo *domain.MessageReply
	Forward *domain.MessageForward
}

// String 展开指针字段，失败信息里直接看得到差异。
func (v sentView) String() string {
	out := fmt.Sprintf("from=%+v body=%q", v.From, v.Body)
	if v.SendAs != nil {
		out += fmt.Sprintf(" send_as=%+v", *v.SendAs)
	}
	if v.ReplyTo != nil {
		out += fmt.Sprintf(" reply_to=%+v", *v.ReplyTo)
	}
	if v.Forward != nil {
		out += fmt.Sprintf(" forward=%+v", *v.Forward)
	}
	return out
}

func privateSentView(msg domain.Message) sentView {
	return sentView{From: msg.From, Body: msg.Body, ReplyTo: msg.ReplyTo, Forward: msg.Forward}
}

func channelSentView(msg domain.ChannelMessage) sentView {
	return sentView{From: msg.From, SendAs: msg.SendAs, Body: msg.Body, ReplyTo: msg.ReplyTo, Forward: msg.Forward}
}

// TestScheduledSendMatchesImmediateSend：同一请求立即发送一次、定时排程一次并到点发出，
// 两条消息的发送者、send_as、回复引用与转发头（含 channel_post、saved_from）必须一致。
// 期望值来自立即发送的结果，不手写。
func TestScheduledSendMatchesImmediateSend(t *testing.T) {
	type scenario struct {
		// send 以 randomID 发出同一请求；scheduleDate 非零时排成定时消息。
		send func(randomID int64, scheduleDate int)
		// read 从目标会话历史里读回 randomID 那条消息。
		read func(randomID int64) (sentView, bool)
		// feature 断言立即发送的结果确实带着要比较的那部分（防止两边一起丢）。
		feature func(sentView) bool
	}
	cases := []struct {
		name  string
		setup func(f scheduledFixture) scenario
	}{
		{"reply", func(f scheduledFixture) scenario {
			root := f.sendPrivate(71001, "root")
			return scenario{
				send: func(randomID int64, scheduleDate int) {
					req := &tg.MessagesSendMessageRequest{Peer: f.friendPeer(), Message: "reply", RandomID: randomID}
					req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: root.ID})
					if scheduleDate != 0 {
						req.SetScheduleDate(scheduleDate)
					}
					if _, err := f.router.onMessagesSendMessage(f.ctx(f.owner), req); err != nil {
						f.t.Fatalf("send reply (schedule=%d): %v", scheduleDate, err)
					}
				},
				read: func(randomID int64) (sentView, bool) {
					msg, ok := f.privateByRandomID(randomID)
					return privateSentView(msg), ok
				},
				feature: func(v sentView) bool { return v.ReplyTo != nil && v.ReplyTo.MessageID == root.ID },
			}
		}},
		{"forward", func(f scheduledFixture) scenario {
			source := f.sendPrivate(75001, "forward me")
			return scenario{
				send: func(randomID int64, scheduleDate int) {
					f.forward(&tg.MessagesForwardMessagesRequest{FromPeer: f.friendPeer(), ToPeer: f.friendPeer(), ID: []int{source.ID}, RandomID: []int64{randomID}}, scheduleDate)
				},
				read: func(randomID int64) (sentView, bool) {
					msg, ok := f.privateByRandomID(randomID)
					return privateSentView(msg), ok
				},
				feature: func(v sentView) bool { return v.Forward != nil && v.Forward.Date == source.Date },
			}
		}},
		{"forward to saved messages", func(f scheduledFixture) scenario {
			source := f.sendPrivate(77001, "save me")
			return scenario{
				send: func(randomID int64, scheduleDate int) {
					f.forward(&tg.MessagesForwardMessagesRequest{FromPeer: f.friendPeer(), ToPeer: &tg.InputPeerSelf{}, ID: []int{source.ID}, RandomID: []int64{randomID}}, scheduleDate)
				},
				read: func(randomID int64) (sentView, bool) {
					msg, ok := f.historyByRandomID(domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, randomID)
					return privateSentView(msg), ok
				},
				feature: func(v sentView) bool { return v.Forward != nil && v.Forward.SavedFromMsgID == source.ID },
			}
		}},
		{"forward of a channel post", func(f scheduledFixture) scenario {
			created, err := f.router.onChannelsCreateChannel(f.ctx(f.owner), &tg.ChannelsCreateChannelRequest{Title: "Source Broadcast", Broadcast: true})
			if err != nil {
				f.t.Fatalf("create broadcast: %v", err)
			}
			broadcast := created.(*tg.Updates).Chats[0].(*tg.Channel)
			f.sendInChannel(f.owner, broadcast, 76001, nil, 0)
			post, ok := f.channelByRandomID(broadcast, 76001)
			if !ok {
				f.t.Fatalf("post not in broadcast history")
			}
			group := f.createGroup("Forward Target")
			return scenario{
				send: func(randomID int64, scheduleDate int) {
					f.forward(&tg.MessagesForwardMessagesRequest{FromPeer: inputPeerChannel(broadcast), ToPeer: inputPeerChannel(group), ID: []int{post.ID}, RandomID: []int64{randomID}}, scheduleDate)
				},
				read: func(randomID int64) (sentView, bool) {
					msg, ok := f.channelByRandomID(group, randomID)
					return channelSentView(msg), ok
				},
				feature: func(v sentView) bool { return v.Forward != nil && v.Forward.ChannelPost == post.ID },
			}
		}},
		{"send as the group", func(f scheduledFixture) scenario {
			group := f.createGroup("Scheduled Send As")
			return scenario{
				send: func(randomID int64, scheduleDate int) {
					f.sendInChannel(f.owner, group, randomID, inputPeerChannel(group), scheduleDate)
				},
				read: func(randomID int64) (sentView, bool) {
					msg, ok := f.channelByRandomID(group, randomID)
					return channelSentView(msg), ok
				},
				feature: func(v sentView) bool {
					return v.SendAs != nil && *v.SendAs == (domain.Peer{Type: domain.PeerTypeChannel, ID: group.ID})
				},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newScheduledFixture(t)
			s := tc.setup(f)
			const immediateID, scheduledID = int64(90001), int64(90002)
			s.send(immediateID, 0)
			s.send(scheduledID, f.scheduleDate())
			if _, ok := s.read(scheduledID); ok {
				t.Fatalf("scheduled message is in history before dispatch")
			}
			if !f.dispatch() {
				t.Fatalf("dispatcher claimed nothing")
			}
			want, ok := s.read(immediateID)
			if !ok || !s.feature(want) {
				t.Fatalf("immediate send = %v (found %v), fixture does not exercise the feature", want, ok)
			}
			got, ok := s.read(scheduledID)
			if !ok {
				t.Fatalf("scheduled message was not dispatched")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("dispatched scheduled message = %v\nimmediate send            = %v", got, want)
			}
		})
	}
}

// TestScheduledSendAsRevokedIsNotSentAsSelf 与立即发送一致：发出时已无权以群身份（匿名
// 管理员）发言，SEND_AS_PEER_INVALID，定时消息不发出、留在定时队列里，绝不退化成以本人
// 身份发出暴露匿名者。
func TestScheduledSendAsRevokedIsNotSentAsSelf(t *testing.T) {
	f := newScheduledFixture(t)
	group := f.createGroup("Scheduled Send As Revoked")
	f.setFriendAdminRights(group, tg.ChatAdminRights{Anonymous: true, DeleteMessages: true})
	f.sendInChannel(f.friend, group, 74001, inputPeerChannel(group), f.scheduleDate())
	f.setFriendAdminRights(group, tg.ChatAdminRights{})

	f.dispatch()

	if sent, ok := f.channelByRandomID(group, 74001); ok {
		t.Fatalf("scheduled message sent after send_as was revoked: from=%+v send_as=%+v", sent.From, sent.SendAs)
	}
	if queued := f.scheduledHistory(f.friend, inputPeerChannel(group)); len(queued) != 1 || queued[0].Message != "in group" {
		t.Fatalf("scheduled queue after failed dispatch = %+v, want the message still scheduled", queued)
	}
}

// TestScheduledReplyToDeletedMessageStillSends 按官方行为：回复目标在发出前被删除时，
// 定时消息照常发出、不带回复引用，且不会卡在重试里（定时队列清空）。
func TestScheduledReplyToDeletedMessageStillSends(t *testing.T) {
	f := newScheduledFixture(t)
	root := f.sendPrivate(72001, "root")
	req := &tg.MessagesSendMessageRequest{Peer: f.friendPeer(), Message: "scheduled reply", RandomID: 72002}
	req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: root.ID})
	req.SetScheduleDate(f.scheduleDate())
	if _, err := f.router.onMessagesSendMessage(f.ctx(f.owner), req); err != nil {
		t.Fatalf("schedule reply: %v", err)
	}
	if _, err := f.router.onMessagesDeleteMessages(f.ctx(f.owner), &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{root.ID}}); err != nil {
		t.Fatalf("delete reply target: %v", err)
	}

	f.dispatch()

	sent, ok := f.privateByRandomID(72002)
	if !ok {
		t.Fatalf("scheduled reply to a deleted message was not dispatched (queue %+v)", f.scheduledHistory(f.owner, f.friendPeer()))
	}
	if sent.ReplyTo != nil {
		t.Fatalf("dispatched reply_to = %+v, want no reply header after the target was deleted", sent.ReplyTo)
	}
	if queued := f.scheduledHistory(f.owner, f.friendPeer()); len(queued) != 0 {
		t.Fatalf("scheduled queue after dispatch = %+v, want empty", queued)
	}
}
