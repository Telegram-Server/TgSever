package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// 定时转发频道帖子在到点发出时才给源帖子计转发数：排程时不计，取消的定时转发不计；
// 同一请求里同一帖子只计一次。
func TestScheduledForwardCountsPostWhenSent(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := context.Background()
	created, err := f.router.onChannelsCreateChannel(f.ctx(f.owner), &tg.ChannelsCreateChannelRequest{Title: "Scheduled forwards", About: "posts", Broadcast: true})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	source := created.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := f.router.onMessagesSendMessage(f.ctx(f.owner), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(source), Message: "post", RandomID: 8801})
	if err != nil {
		t.Fatalf("send post: %v", err)
	}
	postID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	group := f.createGroup("Scheduled forward target")
	forwards := func() int {
		t.Helper()
		got, err := f.channels.GetChannelMessages(ctx, f.owner.ID, source.ID, []int{postID})
		if err != nil || len(got.Messages) != 1 {
			t.Fatalf("get post: %+v, %v", got.Messages, err)
		}
		return got.Messages[0].ForwardsCount
	}

	date := f.scheduleDate()
	f.forward(&tg.MessagesForwardMessagesRequest{FromPeer: inputPeerChannel(source), ToPeer: f.friendPeer(), ID: []int{postID, postID}, RandomID: []int64{8802, 8803}}, date)
	f.forward(&tg.MessagesForwardMessagesRequest{FromPeer: inputPeerChannel(source), ToPeer: inputPeerChannel(group), ID: []int{postID}, RandomID: []int64{8804}}, date)
	f.forward(&tg.MessagesForwardMessagesRequest{FromPeer: inputPeerChannel(source), ToPeer: inputPeerChannel(group), ID: []int{postID}, RandomID: []int64{8805}}, date)
	if got := forwards(); got != 0 {
		t.Fatalf("forwards after scheduling = %d, want 0", got)
	}
	queued := f.scheduledHistory(f.owner, inputPeerChannel(group))
	if len(queued) != 2 {
		t.Fatalf("scheduled in group = %d, want 2", len(queued))
	}
	cancel := max(queued[0].ID, queued[1].ID)
	if _, err := f.router.onMessagesDeleteScheduledMessages(f.ctx(f.owner), &tg.MessagesDeleteScheduledMessagesRequest{Peer: inputPeerChannel(group), ID: []int{cancel}}); err != nil {
		t.Fatalf("cancel scheduled forward: %v", err)
	}

	if !f.dispatch() {
		t.Fatal("dispatcher claimed nothing")
	}
	// 私聊两份同一帖子计一次，群里一份计一次，取消的一份不计。
	if got := forwards(); got != 2 {
		t.Fatalf("forwards after dispatch = %d, want 2", got)
	}
}
