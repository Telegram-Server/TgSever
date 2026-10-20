package rpc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestInviteManagementPermissionsRPC(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	var actors [5]domain.User
	for i := range actors {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(7200 + i), Phone: fmt.Sprintf("1555000720%d", i), FirstName: "Invite actor"})
		if err != nil {
			t.Fatal(err)
		}
		actors[i] = u
	}
	owner, a, b, editor, requester := actors[0], actors[1], actors[2], actors[3], actors[4]
	channels := memory.NewChannelStore()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Permission", Megagroup: true, MemberUserIDs: []int64{a.ID, b.ID, editor.ID}, Date: int(clock.System.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	for _, admin := range []struct {
		user   domain.User
		rights domain.ChannelAdminRights
	}{{a, domain.ChannelAdminRights{InviteUsers: true}}, {b, domain.ChannelAdminRights{InviteUsers: true}}, {editor, domain.ChannelAdminRights{ChangeInfo: true}}} {
		if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner.ID, ChannelID: id, MemberID: admin.user.ID, AdminRights: admin.rights}); err != nil {
			t.Fatal(err)
		}
	}
	r := New(Config{}, Deps{Users: appusers.NewService(users), Channels: appchannels.NewService(channels)}, zaptest.NewLogger(t), clock.System)
	peer := &tg.InputPeerChannel{ChannelID: id, AccessHash: created.Channel.AccessHash}
	aCtx, bCtx, ownerCtx, editorCtx := WithUserID(ctx, a.ID), WithUserID(ctx, b.ID), WithUserID(ctx, owner.ID), WithUserID(ctx, editor.ID)
	result, err := r.onMessagesExportChatInvite(aCtx, &tg.MessagesExportChatInviteRequest{Peer: peer, Title: "Approval", RequestNeeded: true})
	if err != nil {
		t.Fatal(err)
	}
	link := result.(*tg.ChatInviteExported).Link
	if _, ok := result.(*tg.ChatInviteExported).GetStartDate(); !ok {
		t.Fatal("exported invite lacks start_date")
	}

	t.Run("other admin denied all link operations", func(t *testing.T) {
		checks := []struct {
			name string
			run  func() error
		}{
			{"query", func() error {
				_, err := r.onMessagesGetExportedChatInvite(bCtx, &tg.MessagesGetExportedChatInviteRequest{Peer: peer, Link: link})
				return err
			}},
			{"list", func() error {
				_, err := r.onMessagesGetExportedChatInvites(bCtx, &tg.MessagesGetExportedChatInvitesRequest{Peer: peer, AdminID: &tg.InputUser{UserID: a.ID, AccessHash: a.AccessHash}, Limit: 10})
				return err
			}},
			{"edit", func() error {
				req := &tg.MessagesEditExportedChatInviteRequest{Peer: peer, Link: link}
				req.SetTitle("Stolen")
				_, err := r.onMessagesEditExportedChatInvite(bCtx, req)
				return err
			}},
			{"revoke", func() error {
				_, err := r.onMessagesEditExportedChatInvite(bCtx, &tg.MessagesEditExportedChatInviteRequest{Peer: peer, Link: link, Revoked: true})
				return err
			}},
			{"delete", func() error {
				_, err := r.onMessagesDeleteExportedChatInvite(bCtx, &tg.MessagesDeleteExportedChatInviteRequest{Peer: peer, Link: link})
				return err
			}},
			{"delete revoked", func() error {
				_, err := r.onMessagesDeleteRevokedExportedChatInvites(bCtx, &tg.MessagesDeleteRevokedExportedChatInvitesRequest{Peer: peer, AdminID: &tg.InputUser{UserID: a.ID, AccessHash: a.AccessHash}})
				return err
			}},
		}
		for _, check := range checks {
			t.Run(check.name, func(t *testing.T) {
				if err := check.run(); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
					t.Fatalf("error=%v", err)
				}
			})
		}
		actual, err := r.onMessagesGetExportedChatInvite(ownerCtx, &tg.MessagesGetExportedChatInviteRequest{Peer: peer, Link: link})
		if err != nil {
			t.Fatal(err)
		}
		invite := actual.(*tg.MessagesExportedChatInvite).Invite.(*tg.ChatInviteExported)
		if invite.Title != "Approval" || invite.Revoked {
			t.Fatalf("link changed after denied calls: %+v", invite)
		}
	})
	t.Run("change info cannot invite or approve", func(t *testing.T) {
		if _, err := r.onMessagesExportChatInvite(editorCtx, &tg.MessagesExportChatInviteRequest{Peer: peer}); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
			t.Fatalf("export error=%v", err)
		}
		hash, err := channelInviteHashFromLink(link)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, requester.ID), hash); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
			t.Fatalf("request error=%v", err)
		}
		full, err := r.onChannelsGetFullChannel(editorCtx, &tg.InputChannel{ChannelID: id, AccessHash: created.Channel.AccessHash})
		if err != nil {
			t.Fatal(err)
		}
		info := full.FullChat.(*tg.ChannelFull)
		if count, present := info.GetRequestsPending(); present && count != 0 {
			t.Fatalf("ChangeInfo-only admin saw pending count %d", count)
		}
		if recent, _ := info.GetRecentRequesters(); len(recent) != 0 {
			t.Fatalf("ChangeInfo-only admin saw requesters %v", recent)
		}
		for _, user := range full.Users {
			if user, ok := user.(*tg.User); ok && user.ID == requester.ID {
				t.Fatal("pending requester identity leaked through channelFull users")
			}
		}
		if _, err := r.onMessagesHideChatJoinRequest(editorCtx, &tg.MessagesHideChatJoinRequestRequest{Peer: peer, Approved: true, UserID: &tg.InputUser{UserID: requester.ID, AccessHash: requester.AccessHash}}); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
			t.Fatalf("approve error=%v", err)
		}
	})
	t.Run("owner can edit another admin link", func(t *testing.T) {
		req := &tg.MessagesEditExportedChatInviteRequest{Peer: peer, Link: link}
		req.SetTitle("Owner edit")
		res, err := r.onMessagesEditExportedChatInvite(ownerCtx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.(*tg.MessagesExportedChatInvite).Invite.(*tg.ChatInviteExported).Title != "Owner edit" {
			t.Fatal("owner edit not reflected")
		}
	})
	t.Run("parameter boundaries and unicode title", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			req  tg.MessagesExportChatInviteRequest
			code string
		}{
			{"expired", tg.MessagesExportChatInviteRequest{ExpireDate: int(clock.System.Now().Unix()) - 1}, "EXPIRE_DATE_INVALID"},
			{"negative limit", tg.MessagesExportChatInviteRequest{UsageLimit: -1}, "USAGE_LIMIT_INVALID"},
			{"oversize limit", tg.MessagesExportChatInviteRequest{UsageLimit: 100000}, "USAGE_LIMIT_INVALID"},
			{"approval with limit", tg.MessagesExportChatInviteRequest{RequestNeeded: true, UsageLimit: 1}, "USAGE_LIMIT_INVALID"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := tc.req
				req.Peer = peer
				if _, err := r.onMessagesExportChatInvite(ownerCtx, &req); err == nil || !strings.Contains(err.Error(), tc.code) {
					t.Fatalf("error=%v, want %s", err, tc.code)
				}
			})
		}
		res, err := r.onMessagesExportChatInvite(ownerCtx, &tg.MessagesExportChatInviteRequest{Peer: peer, Title: strings.Repeat("链", domain.MaxChannelInviteTitleLength)})
		if err != nil {
			t.Fatal(err)
		}
		if res.(*tg.ChatInviteExported).Title != strings.Repeat("链", domain.MaxChannelInviteTitleLength) {
			t.Fatal("unicode title changed")
		}
	})
}
