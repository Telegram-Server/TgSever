package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 频道帖子被转发后，帖子本体与 messages.getMessagesViews 都带上转发数；一次转发请求里
// 同一条源帖子出现多次只计一次。
func TestChannelPostForwardsAppearOnPostAndViews(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 35, Phone: "15550002035", FirstName: "Owner"})
	reader, _ := userStore.Create(ctx, domain.User{AccessHash: 36, Phone: "15550002036", FirstName: "Reader"})
	channelStore := memory.NewChannelStore()
	messageStore := memory.NewMessageStore()
	messageStore.AttachChannelStore(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Messages: appmessages.NewService(messageStore, nil),
	}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)
	createChannel := func(title string, broadcast bool) *tg.Channel {
		t.Helper()
		created, err := r.onChannelsCreateChannel(ownerCtx, &tg.ChannelsCreateChannelRequest{
			Title: title, About: "forwards", Broadcast: broadcast, Megagroup: !broadcast,
		})
		if err != nil {
			t.Fatalf("create channel %q: %v", title, err)
		}
		return created.(*tg.Updates).Chats[0].(*tg.Channel)
	}
	source := createChannel("Source", true)
	target := createChannel("Target group", false)
	sourcePeer := &tg.InputPeerChannel{ChannelID: source.ID, AccessHash: source.AccessHash}
	sent, err := r.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
		Peer: sourcePeer, Message: "post", RandomID: 4101,
	})
	if err != nil {
		t.Fatalf("send post: %v", err)
	}
	postID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID

	if _, err := r.onMessagesForwardMessages(ownerCtx, &tg.MessagesForwardMessagesRequest{
		FromPeer: sourcePeer,
		ToPeer:   &tg.InputPeerUser{UserID: reader.ID, AccessHash: reader.AccessHash},
		ID:       []int{postID, postID},
		RandomID: []int64{4102, 4103},
	}); err != nil {
		t.Fatalf("forward post twice to user: %v", err)
	}
	if _, err := r.onMessagesForwardMessages(ownerCtx, &tg.MessagesForwardMessagesRequest{
		FromPeer: sourcePeer,
		ToPeer:   &tg.InputPeerChannel{ChannelID: target.ID, AccessHash: target.AccessHash},
		ID:       []int{postID},
		RandomID: []int64{4104},
	}); err != nil {
		t.Fatalf("forward post to group: %v", err)
	}

	got, err := r.onChannelsGetMessages(ownerCtx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: source.ID, AccessHash: source.AccessHash},
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: postID}},
	})
	if err != nil {
		t.Fatalf("channels.getMessages: %v", err)
	}
	post := got.(interface{ GetMessages() []tg.MessageClass }).GetMessages()[0].(*tg.Message)
	if forwards, ok := post.GetForwards(); !ok || forwards != 2 {
		t.Fatalf("post forwards = %d (set %v), want 2", forwards, ok)
	}

	views, err := r.onMessagesGetMessagesViews(ownerCtx, &tg.MessagesGetMessagesViewsRequest{
		Peer: sourcePeer, ID: []int{postID},
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews: %v", err)
	}
	if forwards, ok := views.Views[0].GetForwards(); !ok || forwards != 2 {
		t.Fatalf("views forwards = %d (set %v), want 2", forwards, ok)
	}
}
