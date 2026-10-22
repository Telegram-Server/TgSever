package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	channelsapp "telesrv/internal/app/channels"
	"telesrv/internal/app/guestauth"
	messagesapp "telesrv/internal/app/messages"
	"telesrv/internal/domain"
)

func TestGuestSenderPostgresPreservesProvenanceAndPermissions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Unix(1900000000, 0)
	users := NewUserStore(pool)
	ids := make([]int64, 3)
	for i := range ids {
		user, err := users.Create(ctx, domain.User{AccessHash: int64(700 + i), Phone: fmt.Sprintf("+1998%s%d", randomSuffix(t), i), FirstName: fmt.Sprintf("Guest%d", i), Bot: i == 2})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = user.ID
	}
	caller, recipient, bot := ids[0], ids[1], ids[2]
	queries := NewBotGuestQueryStore(pool)
	nextID := int64(80)
	grant := func(peer domain.Peer, actor int64) context.Context {
		t.Helper()
		nextID++
		q := domain.BotGuestQuery{ID: nextID, BotUserID: bot, CallerUserID: actor, Peer: peer, MessageID: 1, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := queries.CreateGuestQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, _, err := guestauth.Consume(ctx, queries, recipient, q.ID, now); err == nil {
			t.Fatal("wrong bot redeemed persisted grant")
		}
		sendCtx, _, err := guestauth.Consume(ctx, queries, bot, q.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := guestauth.Consume(ctx, queries, bot, q.ID, now); err == nil {
			t.Fatal("persisted grant replay")
		}
		return sendCtx
	}
	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	svc := messagesapp.NewService(messages, nil)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient}
	sent, err := svc.SendPrivateText(grant(peer, caller), caller, domain.SendPrivateTextRequest{RecipientUserID: recipient, RandomID: 91, Message: "guest answer", Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	assertGuest := func(msg domain.Message) {
		t.Helper()
		if msg.From.ID != bot || msg.ViaBotID != bot || msg.GuestChatViaFrom != domain.GuestChatPeer(caller) {
			t.Fatalf("durable guest attribution: %+v", msg)
		}
	}
	assertGuest(sent.SenderMessage)
	assertGuest(sent.RecipientMessage)
	history, err := messages.ListByUser(ctx, recipient, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: caller}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 1 {
		t.Fatalf("history: %+v", history.Messages)
	}
	assertGuest(history.Messages[0])
	events, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Message.ID == sent.RecipientMessage.ID {
			assertGuest(event.Message)
			found = true
		}
	}
	if !found {
		t.Fatal("guest not delivered in durable updates")
	}
	replay, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: caller, RecipientUserID: recipient, RandomID: 91, Message: "guest answer", Date: int(now.Unix()), GuestBotID: bot, ViaBotID: bot})
	if err != nil || !replay.Duplicate {
		t.Fatalf("guest receipt: %+v %v", replay, err)
	}
	assertGuest(replay.SenderMessage)
	copied, err := messages.ForwardPrivateMessages(ctx, domain.ForwardPrivateMessagesRequest{OwnerUserID: caller, FromPeer: peer, ToUserID: caller, MessageIDs: []int{sent.SenderMessage.ID}, RandomIDs: []int64{97}, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.SenderMessages) != 1 || copied.SenderMessages[0].GuestChatViaFrom.ID != 0 || copied.SenderMessages[0].From.ID != caller || copied.SenderMessages[0].Forward == nil || copied.SenderMessages[0].Forward.From.ID != bot {
		t.Fatalf("saved forward inherited guest authorization: %+v", copied)
	}
	ordinary, err := svc.SendPrivateText(ctx, caller, domain.SendPrivateTextRequest{RecipientUserID: recipient, RandomID: 92, Message: "ordinary inline", ViaBotID: bot})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.SenderMessage.GuestChatViaFrom.ID != 0 {
		t.Fatal("ordinary inline misclassified")
	}
	business, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: caller, RecipientUserID: recipient, RandomID: 93, Message: "business answer", BusinessConnectionID: "connection-real", BusinessBotID: bot})
	if err != nil {
		t.Fatal(err)
	}
	restored, found, err := messages.GetByUID(ctx, recipient, business.RecipientMessage.UID)
	if err != nil || !found {
		t.Fatalf("load business provenance: %v", err)
	}
	if restored.BusinessConnectionID != "connection-real" || restored.BusinessBotID != bot || restored.GuestChatViaFrom.ID != 0 {
		t.Fatalf("business provenance: %+v", restored)
	}
	dialogs, err := NewDialogStore(pool).ListByUser(ctx, recipient, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, msg := range dialogs.Messages {
		if msg.UID == business.RecipientMessage.UID {
			found = true
			if msg.BusinessConnectionID != "connection-real" || msg.BusinessBotID != bot {
				t.Fatalf("dialog provenance: %+v", msg)
			}
		}
	}
	if !found {
		t.Fatal("business message missing from dialog")
	}
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: caller, MemberUserIDs: []int64{recipient}, Title: "guest group", Megagroup: true, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	channelSvc := channelsapp.NewService(channels)
	group, err := channelSvc.SendMessage(grant(channelPeer, recipient), recipient, domain.SendChannelMessageRequest{ChannelID: channelPeer.ID, RandomID: 94, Message: "guest group answer", Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := channels.GetChannelMessages(ctx, caller, channelPeer.ID, []int{group.Message.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].From.ID != bot || loaded.Messages[0].GuestChatViaFrom.ID != recipient || loaded.Messages[0].SenderUserID != recipient {
		t.Fatalf("group provenance: %+v", loaded.Messages)
	}
	if _, err := channelSvc.SendMessage(ctx, bot, domain.SendChannelMessageRequest{ChannelID: channelPeer.ID, RandomID: 95, Message: "ordinary bot"}); err == nil {
		t.Fatal("guest acquired persistent membership")
	}
	_, err = channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: caller, ChannelID: channelPeer.ID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: bot}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channelSvc.SendMessage(grant(channelPeer, recipient), recipient, domain.SendChannelMessageRequest{ChannelID: channelPeer.ID, RandomID: 96, Message: "banned bot", Date: int(now.Unix())}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("banned guest: %v", err)
	}
}
