package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelRejoinKeepsRestrictions 校验被限制（有部分禁止位、没有 view_messages 禁令）的成员
// 退群后无论经哪条路径重新入群，原限制与期限都保持不变；期限已过的限制视为无限制。
// users：群主、只有邀请权的管理员 A、被限制成员 R、普通成员 N。
func ChannelRejoinKeepsRestrictions(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, r, n := users[0], users[1], users[2], users[3]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Rejoin restrictions", Megagroup: true, MemberUserIDs: []int64{a, r, n}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: a, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: baseNow()}); err != nil {
		t.Fatal(err)
	}
	restrict := func(target int64, rights domain.ChannelBannedRights) {
		t.Helper()
		if _, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: baseNow()}); err != nil {
			t.Fatal(err)
		}
	}
	leave := func(target int64) {
		t.Helper()
		if _, err := s.LeaveChannel(ctx, id, target, baseNow()); err != nil {
			t.Fatal(err)
		}
	}
	participant := func(target int64) domain.ChannelMember {
		t.Helper()
		m, err := s.GetParticipant(ctx, owner, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	expectRights := func(step string, res domain.CreateChannelResult, target int64, want domain.ChannelBannedRights) {
		t.Helper()
		found := false
		for _, m := range res.Members {
			if m.UserID != target {
				continue
			}
			found = true
			if m.BannedRights != want {
				t.Fatalf("%s: result member rights = %+v, want %+v", step, m.BannedRights, want)
			}
		}
		if !found {
			t.Fatalf("%s: %d missing from result members %+v", step, target, res.Members)
		}
		m := participant(target)
		if m.Status != domain.ChannelMemberActive || m.BannedRights != want {
			t.Fatalf("%s: stored participant = %+v, want active with %+v", step, m, want)
		}
	}

	limited := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: baseNow() + 3600}
	restrict(r, limited)

	// 1. 被只有邀请权的管理员重新拉回：限制与期限保留。
	leave(r)
	invited, err := s.InviteToChannel(ctx, id, a, []int64{r}, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	expectRights("invite", invited, r, limited)

	// 2. 经入群申请被批准：限制与期限保留。
	export := func(requestNeeded bool) string {
		t.Helper()
		res, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, RequestNeeded: requestNeeded, Title: "rejoin", Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		return res.Invite.Hash
	}
	requestHash := export(true)
	request := func(target int64) {
		t.Helper()
		if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: target, Hash: requestHash, Date: baseNow()}); !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("join request of %d = %v", target, err)
		}
	}
	leave(r)
	request(r)
	approved, err := s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: id, TargetUserID: r, Approved: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	expectRights("approve request", approved, r, limited)

	leave(r)
	request(r)
	approvedAll, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Limit: 10, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	expectRights("approve all requests", approvedAll, r, limited)

	// 3. 经不需审批的邀请链接加入：限制与期限保留。
	leave(r)
	imported, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: r, Hash: export(false), Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	expectRights("import invite", imported, r, limited)

	// 4. 自行加入（公开群）：限制与期限保留。
	leave(r)
	joined, err := s.JoinChannel(ctx, id, r, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	expectRights("join", joined, r, limited)

	// 5. 期限已过的限制视为无限制：任何路径重新入群后都不再带着它。
	expiring := domain.ChannelBannedRights{SendMessages: true, UntilDate: baseNow() + 60}
	later := baseNow() + 120
	restrict(r, expiring)
	leave(r)
	invited, err = s.InviteToChannel(ctx, id, a, []int64{r}, later)
	if err != nil {
		t.Fatal(err)
	}
	expectRights("invite after expiry", invited, r, domain.ChannelBannedRights{})

	restrict(r, expiring)
	leave(r)
	request(r)
	approved, err = s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: id, TargetUserID: r, Approved: true, Date: later})
	if err != nil {
		t.Fatal(err)
	}
	expectRights("approve after expiry", approved, r, domain.ChannelBannedRights{})

	restrict(r, expiring)
	leave(r)
	joined, err = s.JoinChannel(ctx, id, r, later)
	if err != nil {
		t.Fatal(err)
	}
	expectRights("join after expiry", joined, r, domain.ChannelBannedRights{})

	// 6. 从未受限的成员退群再被拉回，仍然没有任何限制。
	leave(n)
	invited, err = s.InviteToChannel(ctx, id, a, []int64{n}, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	expectRights("invite unrestricted", invited, n, domain.ChannelBannedRights{})

	// 7. 被踢出者维持现有逻辑：不能自行加入，没有封禁权的管理员拉不回，
	// 有封禁权的管理员拉回等同解封。
	restrict(n, domain.ChannelBannedRights{ViewMessages: true, SendMessages: true})
	if _, err := s.JoinChannel(ctx, id, n, baseNow()); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("kicked join = %v", err)
	}
	if _, err := s.InviteToChannel(ctx, id, a, []int64{n}, baseNow()); !errors.Is(err, domain.ErrUserKicked) {
		t.Fatalf("kicked invite without ban right = %v", err)
	}
	invited, err = s.InviteToChannel(ctx, id, owner, []int64{n}, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	expectRights("invite kicked by owner", invited, n, domain.ChannelBannedRights{})

	// 8. 批量拉人逐个保留各自的限制。
	restrict(r, limited)
	leave(r)
	leave(n)
	invited, err = s.InviteToChannel(ctx, id, a, []int64{r, n}, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	expectRights("batch invite restricted", invited, r, limited)
	expectRights("batch invite unrestricted", invited, n, domain.ChannelBannedRights{})
}
