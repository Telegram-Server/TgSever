package rpc

import (
	"context"
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

// scheduledFidelityFixture 是真实内存用户/频道/消息服务 + 与 postgres 同语义的定时队列：
// 定时消息排程、到点发出都走生产代码路径，发出结果从真实历史里读回。
type scheduledFidelityFixture struct {
	t        *testing.T
	router   *Router
	messages *albumScheduledMessages
	channels *memory.ChannelStore
	owner    domain.User
	friend   domain.User
}

func newScheduledFidelityFixture(t *testing.T) scheduledFidelityFixture {
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
	messages := &albumScheduledMessages{
		appMessagesBase: appMessagesBase{appmessages.NewService(memory.NewMessageStore(dialogStore), dialogStore)},
		scheduledQueue:  &scheduledQueue{},
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Messages: messages,
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	return scheduledFidelityFixture{t: t, router: r, messages: messages, channels: channelStore, owner: owner, friend: friend}
}

func (f scheduledFidelityFixture) ctx(user domain.User) context.Context {
	return WithUserID(context.Background(), user.ID)
}

func (f scheduledFidelityFixture) friendPeer() *tg.InputPeerUser {
	return &tg.InputPeerUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash}
}

func scheduledFidelityDate() int {
	return int(time.Now().Unix()) + 3600
}

// dispatch 让后台派发器跑一轮（测试队列里所有 pending 都视为到期）。
func (f scheduledFidelityFixture) dispatch() {
	f.t.Helper()
	NewScheduledDispatcher(f.router, zaptest.NewLogger(f.t)).DispatchOnce(context.Background())
}

// privateByRandomID 从 owner 与 friend 的私聊历史里找 random_id 对应的消息。
func (f scheduledFidelityFixture) privateByRandomID(randomID int64) (domain.Message, bool) {
	f.t.Helper()
	history, err := f.messages.GetHistory(context.Background(), f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.friend.ID},
		Limit:   50,
	})
	if err != nil {
		f.t.Fatalf("private history: %v", err)
	}
	for _, msg := range history.Messages {
		if msg.RandomID == randomID {
			return msg, true
		}
	}
	return domain.Message{}, false
}

func (f scheduledFidelityFixture) sendPrivate(randomID int64, text string) domain.Message {
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

func (f scheduledFidelityFixture) scheduleReply(randomID int64, replyTo int) {
	f.t.Helper()
	req := &tg.MessagesSendMessageRequest{Peer: f.friendPeer(), Message: "scheduled reply", RandomID: randomID}
	req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: replyTo})
	req.SetScheduleDate(scheduledFidelityDate())
	if _, err := f.router.onMessagesSendMessage(f.ctx(f.owner), req); err != nil {
		f.t.Fatalf("schedule reply: %v", err)
	}
}

func TestScheduledReplyIsStillAReplyWhenDispatched(t *testing.T) {
	f := newScheduledFidelityFixture(t)
	root := f.sendPrivate(71001, "root")
	f.scheduleReply(71002, root.ID)

	f.dispatch()

	sent, ok := f.privateByRandomID(71002)
	if !ok {
		t.Fatalf("scheduled reply was not dispatched")
	}
	if sent.ReplyTo == nil || sent.ReplyTo.MessageID != root.ID {
		t.Fatalf("dispatched reply_to = %+v, want reply to %d", sent.ReplyTo, root.ID)
	}
}

// createGroup 由 owner 建一个含 friend 的超级群。
func (f scheduledFidelityFixture) createGroup(title string) *tg.Channel {
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
func (f scheduledFidelityFixture) channelByRandomID(channel *tg.Channel, randomID int64) (domain.ChannelMessage, bool) {
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

func (f scheduledFidelityFixture) scheduleInChannel(user domain.User, channel *tg.Channel, randomID int64, sendAs tg.InputPeerClass) {
	f.t.Helper()
	req := &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(channel), Message: "scheduled", RandomID: randomID}
	if sendAs != nil {
		req.SetSendAs(sendAs)
	}
	req.SetScheduleDate(scheduledFidelityDate())
	if _, err := f.router.onMessagesSendMessage(f.ctx(user), req); err != nil {
		f.t.Fatalf("schedule in channel: %v", err)
	}
}

func TestScheduledSendAsChannelIsKeptWhenDispatched(t *testing.T) {
	f := newScheduledFidelityFixture(t)
	group := f.createGroup("Scheduled Send As")
	f.scheduleInChannel(f.owner, group, 73001, inputPeerChannel(group))

	f.dispatch()

	sent, ok := f.channelByRandomID(group, 73001)
	if !ok {
		t.Fatalf("scheduled send_as message was not dispatched (row state %q)", f.scheduledRow(73001).State)
	}
	if sent.SendAs == nil || *sent.SendAs != (domain.Peer{Type: domain.PeerTypeChannel, ID: group.ID}) {
		t.Fatalf("dispatched send_as = %+v, want the group %d itself", sent.SendAs, group.ID)
	}
}

func (f scheduledFidelityFixture) setFriendAdminRights(group *tg.Channel, rights tg.ChatAdminRights) {
	f.t.Helper()
	if _, err := f.router.onChannelsEditAdmin(f.ctx(f.owner), &tg.ChannelsEditAdminRequest{
		Channel:     inputChannel(group),
		UserID:      &tg.InputUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash},
		AdminRights: rights,
	}); err != nil {
		f.t.Fatalf("edit friend admin rights: %v", err)
	}
}

// TestScheduledSendAsRevokedIsNotSentAsSelf 与立即发送一致：发出时已无权以群身份（匿名
// 管理员）发言，SEND_AS_PEER_INVALID，定时消息不发出，绝不退化成以本人身份发出暴露匿名者。
func TestScheduledSendAsRevokedIsNotSentAsSelf(t *testing.T) {
	f := newScheduledFidelityFixture(t)
	group := f.createGroup("Scheduled Send As Revoked")
	f.setFriendAdminRights(group, tg.ChatAdminRights{Anonymous: true, DeleteMessages: true})
	f.scheduleInChannel(f.friend, group, 74001, inputPeerChannel(group))
	f.setFriendAdminRights(group, tg.ChatAdminRights{})

	f.dispatch()

	if sent, ok := f.channelByRandomID(group, 74001); ok {
		t.Fatalf("scheduled message sent after send_as was revoked: from=%+v send_as=%+v", sent.From, sent.SendAs)
	}
	if row := f.scheduledRow(74001); row.State != "pending" {
		t.Fatalf("scheduled row state = %q, want released back to pending", row.State)
	}
}

// scheduledRow 返回 owner 定时队列里 random_id 对应的行（含已发出/已删除）。
func (f scheduledFidelityFixture) scheduledRow(randomID int64) domain.ScheduledMessage {
	f.t.Helper()
	for _, row := range f.messages.snapshot() {
		if row.RandomID == randomID {
			return row
		}
	}
	f.t.Fatalf("scheduled row random_id=%d not found", randomID)
	return domain.ScheduledMessage{}
}

// TestScheduledReplyToDeletedMessageStillSends 按官方行为：回复目标在发出前被删除时，
// 定时消息照常发出、不带回复引用，且不会卡在重试里。
func TestScheduledReplyToDeletedMessageStillSends(t *testing.T) {
	f := newScheduledFidelityFixture(t)
	root := f.sendPrivate(72001, "root")
	f.scheduleReply(72002, root.ID)
	if _, err := f.router.onMessagesDeleteMessages(f.ctx(f.owner), &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{root.ID}}); err != nil {
		t.Fatalf("delete reply target: %v", err)
	}

	f.dispatch()

	sent, ok := f.privateByRandomID(72002)
	if !ok {
		t.Fatalf("scheduled reply to a deleted message was not dispatched (row state %q)", f.scheduledRow(72002).State)
	}
	if sent.ReplyTo != nil {
		t.Fatalf("dispatched reply_to = %+v, want no reply header after the target was deleted", sent.ReplyTo)
	}
	if row := f.scheduledRow(72002); row.State != "sent" || row.SentMessageID != sent.ID {
		t.Fatalf("scheduled row = state %q sent_id %d, want sent as %d", row.State, row.SentMessageID, sent.ID)
	}
}
