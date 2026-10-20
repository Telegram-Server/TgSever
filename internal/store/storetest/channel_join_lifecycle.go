package storetest

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

// seedStale models an already-active member with a legacy pending importer.
type SeedStaleJoinRequest func(channelID int64, invite domain.ChannelInvite, userID int64, date int)

func ChannelJoinRequestLifecycle(t *testing.T, s store.ChannelStore, users [4]int64, seedStale SeedStaleJoinRequest) {
	t.Helper()
	ctx := context.Background()
	owner, first, second, history := users[0], users[1], users[2], users[3]
	makeChannel := func(t *testing.T) int64 {
		t.Helper()
		created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Join lifecycle", Megagroup: true, Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		return created.Channel.ID
	}
	export := func(t *testing.T, id int64, approval bool) domain.ChannelInvite {
		t.Helper()
		result, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, RequestNeeded: approval, Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		return result.Invite
	}
	request := func(t *testing.T, invite domain.ChannelInvite, userID int64, date int) {
		t.Helper()
		_, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: userID, Hash: invite.Hash, Date: date})
		if !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("request: %v", err)
		}
	}
	assertPending := func(t *testing.T, id int64, count int) {
		t.Helper()
		pending, err := s.PendingJoinRequests(ctx, id, 5)
		if err != nil || pending.Count != count {
			t.Fatalf("pending=%+v err=%v want=%d", pending, err, count)
		}
	}
	t.Run("direct invitation removes pending then batch retains remaining join", func(t *testing.T) {
		id := makeChannel(t)
		link := export(t, id, true)
		request(t, link, first, baseNow())
		request(t, link, second, baseNow()+1)
		direct, err := s.InviteToChannel(ctx, id, owner, []int64{second}, baseNow()+2)
		if err != nil || !direct.PendingRequestsChanged {
			t.Fatalf("direct=%+v err=%v", direct, err)
		}
		assertPending(t, id, 1)
		approved, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Date: baseNow() + 3})
		if err != nil || len(approved.Members) != 1 || approved.Members[0].UserID != first || len(approved.Joins) != 1 || approved.Joins[first].Invite == nil || approved.Joins[first].Invite.Hash != link.Hash {
			t.Fatalf("batch=%+v err=%v", approved, err)
		}
		assertPending(t, id, 0)
		actual, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash})
		if err != nil || actual.UsageCount != 1 || actual.RequestedCount != 0 {
			t.Fatalf("link=%+v err=%v", actual, err)
		}
	})
	t.Run("later usage failure rolls back entire batch", func(t *testing.T) {
		id := makeChannel(t)
		link := export(t, id, true)
		request(t, link, first, baseNow())
		request(t, link, second, baseNow()+1)
		if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash, HasRequestNeeded: true, RequestNeeded: false, HasUsageLimit: true, UsageLimit: 1, Date: baseNow() + 2}); err != nil {
			t.Fatal(err)
		}
		before, err := s.GetChannel(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Date: baseNow() + 3})
		if !errors.Is(err, domain.ErrUsersTooMuch) {
			t.Fatalf("batch error=%v", err)
		}
		assertPending(t, id, 2)
		after, err := s.GetChannel(ctx, owner, id)
		if err != nil || after.Channel.TopMessageID != before.Channel.TopMessageID || after.Channel.Pts != before.Channel.Pts || after.Channel.ParticipantsCount != before.Channel.ParticipantsCount {
			t.Fatalf("failure changed channel before=%+v after=%+v err=%v", before.Channel, after.Channel, err)
		}
		for _, userID := range []int64{first, second} {
			member, err := s.GetParticipant(ctx, owner, id, userID)
			if err == nil && member.Status == domain.ChannelMemberActive {
				t.Fatalf("partial membership=%+v", member)
			}
		}
		actual, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash})
		if err != nil || actual.UsageCount != 0 || actual.RequestedCount != 2 {
			t.Fatalf("partial invite=%+v err=%v", actual, err)
		}
	})
	if seedStale != nil {
		t.Run("legacy active pending failure rolls back earlier approval", func(t *testing.T) {
			id := makeChannel(t)
			link := export(t, id, true)
			request(t, link, first, baseNow())
			if _, err := s.InviteToChannel(ctx, id, owner, []int64{second}, baseNow()+1); err != nil {
				t.Fatal(err)
			}
			seedStale(id, link, second, baseNow()+2)
			_, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Date: baseNow() + 3})
			if !errors.Is(err, domain.ErrUserAlreadyParticipant) {
				t.Fatalf("stale batch error=%v", err)
			}
			assertPending(t, id, 2)
			member, err := s.GetParticipant(ctx, owner, id, first)
			if err == nil && member.Status == domain.ChannelMemberActive {
				t.Fatalf("first member partially joined=%+v", member)
			}
			actual, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash})
			if err != nil || actual.UsageCount != 0 || actual.RequestedCount != 2 {
				t.Fatalf("stale partial link=%+v err=%v", actual, err)
			}
		})
	}
	for _, bulk := range []bool{false, true} {
		name := "single delete"
		if bulk {
			name = "bulk delete"
		}
		t.Run(name+" clears pending but preserves joined history", func(t *testing.T) {
			id := makeChannel(t)
			link := export(t, id, false)
			if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: history, Hash: link.Hash, Date: baseNow()}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash, HasRequestNeeded: true, RequestNeeded: true, Date: baseNow() + 1}); err != nil {
				t.Fatal(err)
			}
			request(t, link, first, baseNow()+2)
			if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash, Revoked: true, Date: baseNow() + 3}); err != nil {
				t.Fatal(err)
			}
			var err error
			if bulk {
				err = s.DeleteRevokedExportedInvites(ctx, domain.DeleteRevokedChannelInvitesRequest{UserID: owner, ChannelID: id, AdminUserID: owner})
			} else {
				err = s.DeleteExportedInvite(ctx, domain.DeleteChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Hash})
			}
			if err != nil {
				t.Fatal(err)
			}
			assertPending(t, id, 0)
			joined, err := s.ListInviteImporters(ctx, domain.ChannelInviteImportersRequest{UserID: owner, ChannelID: id, Requested: false})
			if err != nil || len(joined.Importers) != 1 || joined.Importers[0].UserID != history {
				t.Fatalf("joined history=%+v err=%v", joined, err)
			}
			fresh := export(t, id, true)
			request(t, fresh, first, baseNow()+4)
			if _, err := s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: id, TargetUserID: first, Date: baseNow() + 5}); err != nil {
				t.Fatal(err)
			}
			assertPending(t, id, 0)
		})
	}
}
