package storetest

import (
	"context"
	"errors"
	"sync"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

// ChatJoinQueries covers the authority and one-shot boundary against both
// backends, including another administrator racing the assigned guard.
func ChatJoinQueries(t *testing.T, s store.ChannelStore, reopen func() store.ChannelStore, owner, bot, applicant, other int64) {
	t.Helper()
	ctx := context.Background()
	now := 1700000000
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Guarded group", Megagroup: true, MemberUserIDs: []int64{bot, other}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Channel.ID
	for _, id := range []int64{bot, other} {
		if _, err = s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channel, MemberID: id, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: now}); err != nil {
			t.Fatal(err)
		}
	}
	queries := s.(store.ChatJoinQueryStore)
	if _, err = queries.SetChannelGuardBot(ctx, other, channel, bot, true); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("invite-only admin assigned guard: %v", err)
	}
	if _, err = queries.SetChannelGuardBot(ctx, owner, channel, bot, true); err != nil {
		t.Fatal(err)
	}
	invite, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: bot, ChannelID: channel, RequestNeeded: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	submit := func(id int64, date int) domain.ChatJoinQuery {
		t.Helper()
		pending, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: applicant, Hash: invite.Invite.Hash, Date: date})
		if !errors.Is(err, domain.ErrInviteRequestSent) || pending.JoinRequest == nil {
			t.Fatalf("pending=%+v err=%v", pending, err)
		}
		q, err := queries.CreateChatJoinQuery(ctx, domain.ChatJoinQuery{ID: id, ChannelID: channel, BotUserID: bot, UserID: applicant, RequestDate: date, ExpiresAt: date + 10, State: "pending"})
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	query := submit(8001, now+1)
	if _, _, err = queries.ResolveChatJoinQuery(ctx, other, query.ID, "approve", "", now+2); !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
		t.Fatalf("other bot resolved: %v", err)
	}
	if _, _, err = queries.ResolveChatJoinQuery(ctx, bot, query.ID+999, "approve", "", now+2); !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
		t.Fatalf("fabricated query accepted: %v", err)
	}
	reopened := reopen().(store.ChatJoinQueryStore)
	if got, err := reopened.GetChatJoinQuery(ctx, query.ID); err != nil || got.BotUserID != bot || got.UserID != applicant {
		t.Fatalf("query lost on reopen: %+v %v", got, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := queries.ResolveChatJoinQuery(ctx, bot, query.ID, "approve", "", now+3)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("approvals=%d", winners)
	}
	view, err := s.GetChannel(ctx, owner, channel)
	if err != nil || view.Channel.ParticipantsCount != 4 {
		t.Fatalf("approval count=%+v err=%v", view.Channel, err)
	}
	stored, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: bot, ChannelID: channel, Hash: invite.Invite.Hash})
	if err != nil || stored.RequestedCount != 0 || stored.UsageCount != 1 {
		t.Fatalf("invite counters=%+v err=%v", stored, err)
	}
	if _, err = s.LeaveChannel(ctx, channel, applicant, now+4); err != nil {
		t.Fatal(err)
	}
	query = submit(8002, now+20)
	if _, _, err = queries.ResolveChatJoinQuery(ctx, bot, query.ID, "approve", "", now+30); !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
		t.Fatalf("10s boundary accepted: %v", err)
	}
	outcomes, err := queries.ExpireChatJoinQueries(ctx, now+30, 100)
	if err != nil || len(outcomes) != 1 || outcomes[0].Query.State != "queue" || outcomes[0].Result.JoinRequest == nil {
		t.Fatalf("expired handoff=%+v %v", outcomes, err)
	}
	if pending, err := s.PendingJoinRequests(ctx, channel, 10); err != nil || pending.Count != 1 {
		t.Fatalf("handoff removed pending: %+v %v", pending, err)
	}
	if _, err = s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: other, ChannelID: channel, TargetUserID: applicant, Date: now + 31}); err != nil {
		t.Fatal(err)
	}
	query = submit(8003, now+40)
	opened, _, err := queries.ResolveChatJoinQuery(ctx, bot, query.ID, "webview", "https://guard.example/verify", now+41)
	if err != nil || opened.ExpiresAt != now+341 {
		t.Fatalf("webview deadline=%+v %v", opened, err)
	}
	if _, _, err = queries.ResolveChatJoinQuery(ctx, bot, query.ID, "webview", "https://guard.example/other", now+42); !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
		t.Fatalf("duplicate webview accepted: %v", err)
	}
	outcomes, err = queries.ExpireChatJoinQueries(ctx, now+341, 100)
	if err != nil || len(outcomes) != 1 || outcomes[0].Result.JoinRequest.UserID != applicant {
		t.Fatalf("webapp expiry=%+v %v", outcomes, err)
	}
	if _, _, err = queries.ResolveChatJoinQuery(ctx, bot, query.ID, "approve", "", now+342); !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
		t.Fatalf("expired webview resolved: %v", err)
	}
	if _, err = s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: other, ChannelID: channel, TargetUserID: applicant, Date: now + 343}); err != nil {
		t.Fatal(err)
	}
	query = submit(8004, now+400)
	if _, err = s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channel, MemberID: bot, Date: now + 401}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = queries.ResolveChatJoinQuery(ctx, bot, query.ID, "approve", "", now+402); !errors.Is(err, domain.ErrChatJoinQueryInvalid) {
		t.Fatalf("demoted guard resolved query: %v", err)
	}
	if current, err := queries.GetChannelGuardBot(ctx, channel); err != nil || current != 0 {
		t.Fatalf("demoted guard retained assignment: %d %v", current, err)
	}
}
