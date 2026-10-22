package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestGuestProjectionRequiresExplicitProvenance(t *testing.T) {
	caller := domain.Peer{Type: domain.PeerTypeUser, ID: 11}
	bot := domain.Peer{Type: domain.PeerTypeUser, ID: 33}
	ordinary := domain.Message{ID: 1, UID: 8, RandomID: 9, Pts: 1, OwnerUserID: 11, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 22}, From: caller, Out: true, Body: "inline", ViaBotID: 33}
	for _, tc := range []struct {
		name string
		msg  domain.Message
	}{
		{"ordinary inline", ordinary},
		{"bot through itself", domain.Message{ID: 2, Peer: caller, From: bot, ViaBotID: 33, Body: "bot"}},
		{"forward", domain.Message{ID: 3, OwnerUserID: 11, Peer: caller, From: caller, ViaBotID: 33, Forward: &domain.MessageForward{From: bot, Date: 1}, Body: "forward"}},
		{"saved copy", domain.Message{ID: 4, OwnerUserID: 11, Peer: caller, SavedPeer: bot, From: caller, ViaBotID: 33, Out: true, Body: "saved"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projected, ok := tgMessage(tc.msg).(*tg.Message)
			if !ok {
				t.Fatal("not a message")
			}
			if _, found := projected.GetGuestchatViaFrom(); found {
				t.Fatalf("ordinary message misclassified: %+v", projected)
			}
		})
	}
	guest := ordinary
	guest.From = bot
	guest.GuestChatViaFrom = caller
	snapshot, err := store.EncodePrivateSendSnapshot(guest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.DecodePrivateSendSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	projected := tgMessage(replay).(*tg.Message)
	via, ok := projected.GetGuestchatViaFrom()
	if !ok || via.(*tg.PeerUser).UserID != caller.ID || projected.FromID.(*tg.PeerUser).UserID != bot.ID || projected.Out {
		t.Fatalf("guest replay projection: %+v", projected)
	}
	channel := domain.ChannelMessage{ID: 1, ChannelID: 77, SenderUserID: caller.ID, From: bot, Body: "answer", ViaBotID: bot.ID, GuestChatViaFrom: caller}
	group := tgChannelMessage(caller.ID, channel).(*tg.Message)
	via, ok = group.GetGuestchatViaFrom()
	if !ok || via.(*tg.PeerUser).UserID != caller.ID || group.Out || group.FromID.(*tg.PeerUser).UserID != bot.ID {
		t.Fatalf("guest group projection: %+v", group)
	}
}
