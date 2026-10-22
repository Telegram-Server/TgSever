package guestauth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	channelsapp "telesrv/internal/app/channels"
	"telesrv/internal/app/guestauth"
	messagesapp "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestGuestPrivateSendKeepsChatAndExplicitCaller(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1900000000, 0)
	queries := memory.NewBotGuestQueryStore()
	messages := memory.NewMessageStore()
	svc := messagesapp.NewService(messages, nil)
	original, err := svc.SendPrivateText(ctx, 11, domain.SendPrivateTextRequest{RecipientUserID: 22, Message: "@guest question", RandomID: 1, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	q := domain.BotGuestQuery{ID: 41, BotUserID: 33, CallerUserID: 11, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 22}, MessageID: original.SenderMessage.ID, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := queries.CreateGuestQuery(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, _, err := guestauth.Consume(ctx, queries, 34, q.ID, now); err == nil {
		t.Fatal("unrelated bot redeemed grant")
	}
	sendCtx, _, err := guestauth.Consume(ctx, queries, 33, q.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.SendPrivateTextRequest{RecipientUserID: 22, Message: "guest answer", RandomID: 2, Date: int(now.Unix())}
	sent, err := svc.SendPrivateText(sendCtx, 11, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []domain.Message{sent.SenderMessage, sent.RecipientMessage, sent.SenderEvent.Message, sent.RecipientEvent.Message} {
		if msg.From.ID != 33 || msg.GuestChatViaFrom != domain.GuestChatPeer(11) || msg.ViaBotID != 33 {
			t.Fatalf("attribution lost: %+v", msg)
		}
	}
	if sent.SenderMessage.OwnerUserID != 11 || sent.SenderMessage.Peer.ID != 22 || sent.RecipientMessage.OwnerUserID != 22 || sent.RecipientMessage.Peer.ID != 11 {
		t.Fatalf("guest created bot dialog: %+v", sent)
	}
	copied, err := messages.ForwardPrivateMessages(ctx, domain.ForwardPrivateMessagesRequest{OwnerUserID: 11, FromPeer: q.Peer, ToUserID: 11, MessageIDs: []int{sent.SenderMessage.ID}, RandomIDs: []int64{3}, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.SenderMessages) != 1 || copied.SenderMessages[0].GuestChatViaFrom.ID != 0 || copied.SenderMessages[0].From.ID != 11 || copied.SenderMessages[0].Forward == nil || copied.SenderMessages[0].Forward.From.ID != 33 {
		t.Fatalf("saved forward inherited guest authorization: %+v", copied)
	}
	if _, err := svc.SendPrivateText(sendCtx, 11, req); !errors.Is(err, domain.ErrAuthenticatedScopeInvalid) {
		t.Fatalf("reused capability: %v", err)
	}
	if _, _, err := guestauth.Consume(ctx, queries, 33, q.ID, now); err == nil {
		t.Fatal("query replay allowed")
	}
	req.GuestBotID = 33
	if _, err := svc.SendPrivateText(ctx, 11, req); !errors.Is(err, domain.ErrAuthenticatedScopeInvalid) {
		t.Fatalf("forged request: %v", err)
	}
	history, err := messages.ListByUser(ctx, 22, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 11}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var answer domain.Message
	for _, msg := range history.Messages {
		if msg.Body == "guest answer" {
			answer = msg
		}
	}
	if answer.From.ID != 33 || answer.GuestChatViaFrom.ID != 11 {
		t.Fatalf("recipient history: %+v", history.Messages)
	}
}

func TestGuestCapabilityRejectsChangedChatAndExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1900000000, 0)
	queries := memory.NewBotGuestQueryStore()
	q := domain.BotGuestQuery{ID: 51, BotUserID: 33, CallerUserID: 11, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 22}, MessageID: 1, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := queries.CreateGuestQuery(ctx, q); err != nil {
		t.Fatal(err)
	}
	sendCtx, _, err := guestauth.Consume(ctx, queries, 33, q.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	svc := messagesapp.NewService(memory.NewMessageStore(), nil)
	if _, err := svc.SendPrivateText(sendCtx, 11, domain.SendPrivateTextRequest{RecipientUserID: 23, Message: "wrong chat", RandomID: 3}); !errors.Is(err, domain.ErrAuthenticatedScopeInvalid) {
		t.Fatalf("changed chat: %v", err)
	}
	q.ID++
	if err := queries.CreateGuestQuery(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, _, err := guestauth.Consume(ctx, queries, 33, q.ID, q.ExpiresAt); err == nil {
		t.Fatal("grant valid at exclusive expiry boundary")
	}
}

func TestGuestGroupSendDoesNotGrantMembershipOrBypassRestrictions(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1900000000, 0)
	queries := memory.NewBotGuestQueryStore()
	channels := memory.NewChannelStore()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: 11, MemberUserIDs: []int64{22}, Title: "guest group", Megagroup: true, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	svc := channelsapp.NewService(channels)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	nextID := int64(60)
	grant := func(caller int64) context.Context {
		t.Helper()
		nextID++
		q := domain.BotGuestQuery{ID: nextID, BotUserID: 33, CallerUserID: caller, Peer: peer, MessageID: 1, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		if err := queries.CreateGuestQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
		sendCtx, _, err := guestauth.Consume(ctx, queries, 33, q.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		return sendCtx
	}
	req := domain.SendChannelMessageRequest{ChannelID: peer.ID, Message: "guest answer", RandomID: 10, Date: int(now.Unix())}
	sent, err := svc.SendMessage(grant(22), 22, req)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Message.From.ID != 33 || sent.Message.SenderUserID != 22 || sent.Message.GuestChatViaFrom.ID != 22 {
		t.Fatalf("group attribution: %+v", sent.Message)
	}
	if _, err := svc.SendMessage(ctx, 33, domain.SendChannelMessageRequest{ChannelID: peer.ID, Message: "ordinary bot", RandomID: 11}); err == nil {
		t.Fatal("guest reply granted membership to bot")
	}
	if _, err := svc.SendMessage(grant(44), 44, domain.SendChannelMessageRequest{ChannelID: peer.ID, Message: "nonmember caller", RandomID: 12}); err == nil {
		t.Fatal("grant bypassed caller membership")
	}
	_, err = channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: 11, ChannelID: peer.ID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 33}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: int(now.Unix())})
	if err != nil {
		t.Fatal(err)
	}
	req.RandomID = 13
	if _, err := svc.SendMessage(grant(22), 22, req); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("banned guest bot: %v", err)
	}
}
