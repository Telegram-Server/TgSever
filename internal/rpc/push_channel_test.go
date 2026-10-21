package rpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/app/push"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// 接线：群组里一个离线成员登记设备（App 已报后台）之后，另一个成员发消息，
// 推送服务按完整成员列表推送，假发送器收到一条发往该成员 token 的推送。
func TestGroupMemberDeviceReceivesPushForGroupMessage(t *testing.T) {
	ctx := context.Background()
	bobKey := [8]byte{0x98, 0x01}
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	devices := memory.NewPushDeviceStore(authorizations)
	users := memory.NewUserStore()
	channelStore := memory.NewChannelStore()
	alice, err := users.Create(ctx, domain.User{AccessHash: 9801, Phone: "15550009801", FirstName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, domain.User{AccessHash: 9802, Phone: "15550009802", FirstName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authKeys.Save(ctx, store.AuthKeyData{ID: bobKey}); err != nil {
		t.Fatal(err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: bobKey, UserID: bob.ID, Hash: 9802}); err != nil {
		t.Fatal(err)
	}
	usersService := appusers.NewService(users)
	r := New(Config{}, Deps{
		Auth:        appauth.NewService(users, authorizations, nil, authKeys, nil, "12345"),
		Users:       usersService,
		Channels:    appchannels.NewService(channelStore),
		PushDevices: devices,
	}, zaptest.NewLogger(t), clock.System)
	dispatch := func(req bin.Encoder) {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Dispatch(ctx, bobKey, 1, &buf); err != nil {
			t.Fatalf("dispatch %T: %v", req, err)
		}
	}
	const token = "98aa2a0b9e8d7c6b5a4f1c2a0b9e8d7c6b5a4f1c2a0b9e8d7c6b5a4f1c2a0b9e"
	secret := bytes.Repeat([]byte{9}, push.SecretSize)
	dispatch(&tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenAPNs, Token: token, Secret: secret})
	dispatch(&tg.AccountUpdateStatusRequest{Offline: true})

	sender := &capturePushSender{}
	svc := push.NewService(push.Config{}, push.Deps{
		Devices:        devices,
		Authorizations: authorizations,
		Presence:       r,
		Users:          usersService,
		Sender:         sender,
		Members:        channelStore,
	}, zaptest.NewLogger(t))
	r.SetChannelPushNotifier(svc)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(runCtx) }()

	aliceCtx := WithUserID(ctx, alice.ID)
	created, err := r.onMessagesCreateChat(aliceCtx, &tg.MessagesCreateChatRequest{Users: []tg.InputUserClass{inputUser(bob)}, Title: "Team"})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := r.onMessagesSendMessage(aliceCtx, &tg.MessagesSendMessageRequest{
		Peer: inputPeerChannel(channel), Message: "hello team", RandomID: 9800001,
	}); err != nil {
		t.Fatalf("send message: %v", err)
	}
	stop()
	<-done

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d pushes, want 1 (the creation service message must not push)", len(sender.sent))
	}
	if got := sender.sent[0]; got.Token != token || got.Type != push.PushTypeAlert {
		t.Fatalf("push = %+v, want an alert to bob's token", got)
	}
}

func TestChannelMessagePushConversion(t *testing.T) {
	group := domain.Channel{ID: 70, Title: "Team", Megagroup: true, LinkedChatID: 71}
	broadcast := domain.Channel{ID: 71, Title: "News", Broadcast: true, LinkedChatID: 70}
	text := domain.ChannelMessage{ChannelID: 70, ID: 5, SenderUserID: 10, From: domain.Peer{Type: domain.PeerTypeUser, ID: 10}, Body: "hi", Silent: true, GroupedID: 88}

	got, ok := channelMessagePush(10, domain.SendChannelMessageResult{Channel: group, Message: text})
	if !ok || got.Chat != (push.Chat{Kind: push.ChatGroup, ID: 70}) || got.ChatTitle != "Team" || got.SenderID != 10 ||
		got.AuthorName != "" || got.MessageID != 5 || got.Text != "hi" || !got.Silent || got.GroupedID != 88 {
		t.Fatalf("group message = %+v ok=%v", got, ok)
	}

	post := text
	post.ChannelID, post.From = 71, domain.Peer{Type: domain.PeerTypeChannel, ID: 71}
	got, ok = channelMessagePush(10, domain.SendChannelMessageResult{Channel: broadcast, Message: post})
	if !ok || got.Chat != (push.Chat{Kind: push.ChatChannel, ID: 71}) || got.ChatTitle != "News" || got.SenderID != 10 {
		t.Fatalf("channel post = %+v ok=%v", got, ok)
	}

	anonymous := text
	anonymous.From = domain.Peer{Type: domain.PeerTypeChannel, ID: 70}
	if got, ok = channelMessagePush(10, domain.SendChannelMessageResult{Channel: group, Message: anonymous}); !ok || got.AuthorName != "Team" {
		t.Fatalf("anonymous admin = %+v ok=%v, want the group title as author", got, ok)
	}

	skipped := map[string]domain.SendChannelMessageResult{
		"duplicate": {Channel: group, Message: text, Duplicate: true},
		"service":   {Channel: group, Message: withAction(text)},
		"monoforum": {Channel: domain.Channel{ID: 72, Monoforum: true}, Message: text},
		// 讨论组里自动转发的频道帖子：频道成员已因原帖收到推送。
		"discussion copy": {Channel: group, Message: withFrom(text, domain.Peer{Type: domain.PeerTypeChannel, ID: 71})},
		"no id":           {Channel: group, Message: domain.ChannelMessage{ChannelID: 70}},
	}
	for name, res := range skipped {
		if got, ok := channelMessagePush(10, res); ok {
			t.Fatalf("%s: converted to %+v, want skipped", name, got)
		}
	}
}

func withAction(m domain.ChannelMessage) domain.ChannelMessage {
	m.Action = &domain.ChannelMessageAction{}
	return m
}

func withFrom(m domain.ChannelMessage, from domain.Peer) domain.ChannelMessage {
	m.From = from
	return m
}
