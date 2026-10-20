package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestInvitePermanentIdentityRPC(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 7142, Phone: "15550007142", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(users), Channels: appchannels.NewService(memory.NewChannelStore())}, zaptest.NewLogger(t), clock.System)
	ctx = WithUserID(ctx, owner.ID)
	created, err := r.onChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Title: "Invite identity", Megagroup: true})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	t.Run("parameterless export is additional", func(t *testing.T) {
		result, err := r.onMessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{Peer: peer})
		if err != nil {
			t.Fatal(err)
		}
		if result.(*tg.ChatInviteExported).Permanent {
			t.Fatal("parameterless additional invite became permanent")
		}
	})

	t.Run("clearing title preserves additional identity", func(t *testing.T) {
		result, err := r.onMessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{Peer: peer, Title: "Campaign"})
		if err != nil {
			t.Fatal(err)
		}
		req := &tg.MessagesEditExportedChatInviteRequest{Peer: peer, Link: result.(*tg.ChatInviteExported).Link}
		req.SetTitle("")
		edited, err := r.onMessagesEditExportedChatInvite(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		invite := edited.(*tg.MessagesExportedChatInvite).Invite.(*tg.ChatInviteExported)
		if invite.Permanent {
			t.Fatal("clearing additional invite parameters changed its identity")
		}
	})
}
