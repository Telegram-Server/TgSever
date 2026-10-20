package storetest

import (
	"context"
	"telesrv/internal/domain"
	"testing"
)

func BotAPIJoinRequests(t *testing.T, newFixture func(*testing.T) BotAPIMemberUpdateFixture) {
	t.Helper()
	t.Run("snapshot links public requests and filtering", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		chat := BotAPIMemberUpdateChat(f.BotID)
		invite := domain.ChannelInvite{ChannelID: chat.ID, InviteID: 3, Hash: "campaign", AdminUserID: f.BotID, RequestNeeded: true, RequestedCount: 1, Date: baseNow()}
		payload := domain.NewChannelJoinRequest(chat.ID, f.OtherUserID, baseNow(), &invite)
		payload.About = "original bio"
		req := domain.EnqueueBotAPIUpdateRequest{BotUserID: f.BotID, Kind: domain.BotAPIUpdateChatJoinRequest, Peer: chat, Date: payload.Date, JoinRequest: payload}
		first, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
		if err != nil || !created {
			t.Fatalf("default request=%+v created=%v err=%v", first, created, err)
		}
		payload.About, payload.Invite.Hash = "changed", "mutated"
		first.JoinRequest.Invite.Hash = "returned mutation"
		payload.Invite = nil
		second, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
		if err != nil || !created || second.ID <= first.ID {
			t.Fatalf("public request=%+v created=%v err=%v", second, created, err)
		}
		rows, err := f.Store.ListBotAPIUpdates(ctx, f.BotID, first.ID, 100)
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		got := rows[0].JoinRequest
		if got == nil || got.About != "original bio" || got.UserID != f.OtherUserID || got.Invite == nil || got.Invite.Hash != "campaign" || rows[1].JoinRequest.Invite != nil {
			t.Fatalf("snapshot rows=%+v payload=%+v", rows, got)
		}
		tail, err := f.Store.ListTailBotAPIUpdates(ctx, f.BotID, 1, 100)
		if err != nil || len(tail) != 1 || tail[0].JoinRequest == nil || tail[0].ID != second.ID {
			t.Fatalf("tail=%+v err=%v", tail, err)
		}
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessage}); err != nil {
			t.Fatal(err)
		}
		if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err != nil || created {
			t.Fatalf("excluded request created=%v err=%v", created, err)
		}
		if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatJoinRequest}); err != nil {
			t.Fatal(err)
		}
		if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err != nil || !created {
			t.Fatalf("opted-in request created=%v err=%v", created, err)
		}
	})
	t.Run("member invite survives roundtrip and mutation", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		req := BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)
		req.ChatMember.Invite = &domain.ChannelInvite{ChannelID: req.Peer.ID, Hash: "approved", AdminUserID: f.OtherUserID, Date: baseNow()}
		row, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
		if err != nil || !created {
			t.Fatalf("member created=%v err=%v", created, err)
		}
		req.ChatMember.Invite.Hash, row.ChatMember.Invite.Hash = "changed", "returned mutation"
		rows, err := f.Store.ListBotAPIUpdates(ctx, f.BotID, row.ID, 100)
		if err != nil || len(rows) != 1 || rows[0].ChatMember == nil || rows[0].ChatMember.Invite == nil || rows[0].ChatMember.Invite.Hash != "approved" || !rows[0].ChatMember.ViaJoinRequest {
			t.Fatalf("member rows=%+v err=%v", rows, err)
		}
	})
}
