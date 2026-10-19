package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelMemberPreviousStates 校验加入/退出类结果的 Previous 是变化前的真实成员记录：
// 被踢后重新拉入是 kicked、被限制者退群仍带限制、群主退群与接任者各自的旧角色等。
// users：群主、管理员 A、成员 B、成员 C、局外人 D。
func ChannelMemberPreviousStates(t *testing.T, s store.ChannelStore, users [5]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, b, c, d := users[0], users[1], users[2], users[3], users[4]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Previous states", Megagroup: true, MemberUserIDs: []int64{a, b, c}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	must := func(res domain.CreateChannelResult, err error) domain.CreateChannelResult {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	ban := func(target int64, rights domain.ChannelBannedRights) {
		t.Helper()
		if _, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: baseNow()}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(step string, res domain.CreateChannelResult, user int64, status domain.ChannelMemberStatus, role domain.ChannelMemberRole) domain.ChannelMember {
		t.Helper()
		prev := res.PreviousMember(id, user)
		if prev.UserID != user || prev.Status != status || prev.Role != role {
			t.Fatalf("%s: previous of %d = %+v (all %+v), want %s/%s", step, user, prev, res.Previous, status, role)
		}
		return prev
	}
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: a, AdminRights: domain.ChannelAdminRights{BanUsers: true, InviteUsers: true}, Date: baseNow()}); err != nil {
		t.Fatal(err)
	}

	expect("leave", must(s.LeaveChannel(ctx, id, b, baseNow())), b, domain.ChannelMemberActive, domain.ChannelRoleMember)
	expect("join", must(s.JoinChannel(ctx, id, b, baseNow())), b, domain.ChannelMemberLeft, domain.ChannelRoleMember)

	ban(c, domain.ChannelBannedRights{SendMessages: true})
	if prev := expect("restricted leave", must(s.LeaveChannel(ctx, id, c, baseNow())), c, domain.ChannelMemberActive, domain.ChannelRoleMember); !prev.BannedRights.SendMessages {
		t.Fatalf("restricted leave lost banned rights: %+v", prev)
	}

	ban(b, domain.ChannelBannedRights{ViewMessages: true})
	invited := must(s.InviteToChannel(ctx, id, owner, []int64{b, d}, baseNow()))
	expect("re-invite kicked", invited, b, domain.ChannelMemberKicked, domain.ChannelRoleMember)
	if _, ok := invited.Previous[d]; ok {
		t.Fatalf("first-time invitee has a previous record: %+v", invited.Previous[d])
	}
	expect("first invite", invited, d, domain.ChannelMemberLeft, domain.ChannelRoleMember)

	export := func(requestNeeded bool) string {
		t.Helper()
		res, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, RequestNeeded: requestNeeded, Title: "contract", Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		return res.Invite.Hash
	}
	must(s.LeaveChannel(ctx, id, d, baseNow()))
	expect("import invite", must(s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: d, Hash: export(false), Date: baseNow()})), d, domain.ChannelMemberLeft, domain.ChannelRoleMember)

	must(s.LeaveChannel(ctx, id, d, baseNow()))
	requestHash := export(true)
	if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: d, Hash: requestHash, Date: baseNow()}); !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("request invite = %v", err)
	}
	approved := must(s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: id, TargetUserID: d, Approved: true, Date: baseNow()}))
	expect("approve request", approved, d, domain.ChannelMemberLeft, domain.ChannelRoleMember)

	must(s.LeaveChannel(ctx, id, d, baseNow()))
	if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: d, Hash: requestHash, Date: baseNow()}); !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("request invite again = %v", err)
	}
	if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: c, Hash: requestHash, Date: baseNow()}); !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("restricted leaver requests = %v", err)
	}
	all := must(s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: owner, ChannelID: id, Approved: true, Limit: 10, Date: baseNow()}))
	// 批量批准：结果要覆盖每个被批准的人，而不只是最后一个。
	approvedIDs := map[int64]bool{}
	for _, m := range all.Members {
		approvedIDs[m.UserID] = m.Status == domain.ChannelMemberActive
	}
	if len(approvedIDs) != 2 || !approvedIDs[c] || !approvedIDs[d] {
		t.Fatalf("approve all members = %+v", all.Members)
	}
	expect("approve all", all, d, domain.ChannelMemberLeft, domain.ChannelRoleMember)
	if prev := expect("approve all restricted", all, c, domain.ChannelMemberLeft, domain.ChannelRoleMember); !prev.BannedRights.SendMessages {
		t.Fatalf("approve all lost restricted previous: %+v", prev)
	}
}

// ChannelOwnerLeavePrevious 校验群主退群时结果的 Previous 同时带着群主（creator）与接任者
// （原先是 admin）变化前的记录。users：群主、管理员 A、成员 B。
func ChannelOwnerLeavePrevious(t *testing.T, s store.ChannelStore, users [3]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, b := users[0], users[1], users[2]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Owner leave", Megagroup: true, MemberUserIDs: []int64{a, b}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: a, AdminRights: domain.ChannelAdminRights{BanUsers: true, InviteUsers: true}, Date: baseNow()}); err != nil {
		t.Fatal(err)
	}
	ownerLeft, err := s.LeaveChannel(ctx, id, owner, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		step string
		user int64
		role domain.ChannelMemberRole
	}{
		{"owner leave", owner, domain.ChannelRoleCreator},
		{"successor", a, domain.ChannelRoleAdmin},
	} {
		prev := ownerLeft.PreviousMember(id, want.user)
		if prev.UserID != want.user || prev.Status != domain.ChannelMemberActive || prev.Role != want.role {
			t.Fatalf("%s: previous of %d = %+v (all %+v), want active/%s", want.step, want.user, prev, ownerLeft.Previous, want.role)
		}
	}
}

// ChannelAdminProtection 校验管理员保护规则：群主可编辑/封禁任何管理员；
// 非群主管理员只能编辑、降级、封禁或限制由自己提拔的管理员，其余返回 ErrUserAdminInvalid。
// users 至少 4 个：群主、管理员 A（可提拔、可封禁）、群主提拔的管理员 B、A 提拔的管理员 C。
func ChannelAdminProtection(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, b, c := users[0], users[1], users[2], users[3]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Admin protection", Megagroup: true, MemberUserIDs: []int64{a, b, c}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	promote := func(actor, target int64, rights domain.ChannelAdminRights) error {
		_, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: actor, ChannelID: id, MemberID: target, AdminRights: rights, Date: baseNow()})
		return err
	}
	ban := func(actor, target int64, rights domain.ChannelBannedRights) error {
		_, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: actor, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: baseNow()})
		return err
	}
	full := domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, DeleteMessages: true, PinMessages: true}
	if err := promote(owner, a, full); err != nil {
		t.Fatal(err)
	}
	if err := promote(owner, b, domain.ChannelAdminRights{BanUsers: true}); err != nil {
		t.Fatal(err)
	}
	if err := promote(a, c, domain.ChannelAdminRights{BanUsers: true}); err != nil {
		t.Fatal(err)
	}
	role := func(target int64) domain.ChannelMember {
		t.Helper()
		m, err := s.GetParticipant(ctx, owner, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// A 不能动群主提拔的 B：限制、踢出、改权限、撤销。
	if err := ban(a, b, domain.ChannelBannedRights{SendMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("restrict foreign admin = %v", err)
	}
	if err := ban(a, b, domain.ChannelBannedRights{ViewMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("kick foreign admin = %v", err)
	}
	if err := promote(a, b, domain.ChannelAdminRights{BanUsers: true, PinMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("edit foreign admin = %v", err)
	}
	if err := promote(a, b, domain.ChannelAdminRights{}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("demote foreign admin = %v", err)
	}
	if m := role(b); m.Role != domain.ChannelRoleAdmin || m.Status != domain.ChannelMemberActive || !m.AdminRights.BanUsers {
		t.Fatalf("foreign admin changed: %+v", m)
	}
	// B 也不能动 A（A 由群主提拔）。
	if err := ban(b, a, domain.ChannelBannedRights{SendMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("restrict peer admin = %v", err)
	}
	// A 可以编辑、限制自己提拔的 C。
	if err := promote(a, c, domain.ChannelAdminRights{BanUsers: true, PinMessages: true}); err != nil {
		t.Fatalf("edit own admin = %v", err)
	}
	if err := ban(a, c, domain.ChannelBannedRights{SendMessages: true}); err != nil {
		t.Fatalf("restrict own admin = %v", err)
	}
	if m := role(c); m.Role != domain.ChannelRoleMember || !m.BannedRights.SendMessages {
		t.Fatalf("own admin not restricted: %+v", m)
	}
	// 群主可以动任何管理员。
	if err := ban(owner, b, domain.ChannelBannedRights{ViewMessages: true}); err != nil {
		t.Fatalf("owner kick admin = %v", err)
	}
	if err := promote(owner, a, domain.ChannelAdminRights{}); err != nil {
		t.Fatalf("owner demote admin = %v", err)
	}
}
