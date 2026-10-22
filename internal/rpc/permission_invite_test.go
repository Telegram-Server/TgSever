package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

type permissionInviteFixture struct {
	*permissionJoinLeaveFixture
	colleague domain.User
}

func newPermissionInviteFixture(t *testing.T) *permissionInviteFixture {
	t.Helper()
	f := &permissionInviteFixture{permissionJoinLeaveFixture: newPermissionJoinLeaveFixture(t)}
	f.colleague = f.user(9103, "15550009103", "Colleague")
	return f
}

func (f *permissionInviteFixture) invite(ch *tg.Channel, users ...domain.User) *tg.MessagesInvitedUsers {
	f.t.Helper()
	inputs := make([]tg.InputUserClass, 0, len(users))
	for _, u := range users {
		inputs = append(inputs, inputUser(u))
	}
	res, err := f.router.onChannelsInviteToChannel(f.userCtx(f.owner), &tg.ChannelsInviteToChannelRequest{Channel: inputChannel(ch), Users: inputs})
	if err != nil {
		f.t.Fatalf("invite to %q: %v", ch.Title, err)
	}
	return res
}

func (f *permissionInviteFixture) isMember(ch *tg.Channel, user domain.User) bool {
	f.t.Helper()
	view, err := f.router.deps.Channels.ResolveChannel(f.ctx, user.ID, ch.ID)
	return err == nil && !view.Forbidden && view.Self.UserID == user.ID && view.Self.Status == domain.ChannelMemberActive
}

func missingInviteeIDs(res *tg.MessagesInvitedUsers) []int64 {
	ids := make([]int64, 0, len(res.MissingInvitees))
	for _, m := range res.MissingInvitees {
		ids = append(ids, m.UserID)
	}
	return ids
}

func assertMissingInvitees(t *testing.T, res *tg.MessagesInvitedUsers, want ...int64) {
	t.Helper()
	got := missingInviteeIDs(res)
	if len(got) != len(want) {
		t.Fatalf("missing_invitees = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing_invitees = %v, want %v", got, want)
		}
	}
}

func TestPermissionInviteToChannelReportsRestrictedInviteesAsMissing(t *testing.T) {
	t.Run("join group off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		res := f.invite(f.group, f.staff, f.colleague)
		assertMissingInvitees(t, res, f.staff.ID)
		if f.isMember(f.group, f.staff) {
			t.Fatal("restricted staff was added to the group")
		}
		if !f.isMember(f.group, f.colleague) {
			t.Fatal("unrestricted colleague was not added to the group")
		}
		assertMissingInvitees(t, f.invite(f.channel, f.staff))
		if !f.isMember(f.channel, f.staff) {
			t.Fatal("join_group off must not block being added to a broadcast channel")
		}
	})
	t.Run("join channel off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinChannel = false })
		assertMissingInvitees(t, f.invite(f.channel, f.colleague, f.staff), f.staff.ID)
		if f.isMember(f.channel, f.staff) || !f.isMember(f.channel, f.colleague) {
			t.Fatal("channel membership after invite does not match missing_invitees")
		}
		assertMissingInvitees(t, f.invite(f.group, f.staff))
		if !f.isMember(f.group, f.staff) {
			t.Fatal("join_channel off must not block being added to a group")
		}
	})
	t.Run("only restricted invitees returns empty updates", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		assertMissingInvitees(t, f.invite(f.group, f.staff), f.staff.ID)
	})
	t.Run("master switch off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		p := domain.DefaultAccountPermissions(f.staff.ID)
		p.Flags.JoinGroup = false
		f.router.deps.Permissions = &fakePermissionsService{items: map[int64]domain.AccountPermissions{f.staff.ID: p}}
		assertMissingInvitees(t, f.invite(f.group, f.staff, f.colleague))
		if !f.isMember(f.group, f.staff) {
			t.Fatal("master switch off: staff must be added as before")
		}
	})
}

// requestToJoin 让 users 通过需要审批的邀请链接对 ch 发出入群申请（申请时不受限制）。
func (f *permissionInviteFixture) requestToJoin(ch *tg.Channel, users ...domain.User) {
	f.t.Helper()
	hash := f.inviteHash(ch, true)
	for _, u := range users {
		_, err := f.router.onMessagesImportChatInvite(f.userCtx(u), hash)
		if rpcErr, ok := tgerr.As(err); !ok || rpcErr.Type != "INVITE_REQUEST_SENT" {
			f.t.Fatalf("request to join %q as %s: %v", ch.Title, u.FirstName, err)
		}
	}
}

func (f *permissionInviteFixture) approve(ch *tg.Channel, user domain.User, approved bool) error {
	_, err := f.router.onMessagesHideChatJoinRequest(f.userCtx(f.owner), &tg.MessagesHideChatJoinRequestRequest{
		Peer: inputPeerChannel(ch), UserID: inputUser(user), Approved: approved,
	})
	return err
}

func TestPermissionApproveJoinRequestOfRestrictedApplicant(t *testing.T) {
	t.Run("join group off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.group, f.staff)
		f.requestToJoin(f.channel, f.staff)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		assertRPCError(t, "messages.hideChatJoinRequest", f.approve(f.group, f.staff, true), 403, "USER_PRIVACY_RESTRICTED")
		if f.isMember(f.group, f.staff) {
			t.Fatal("restricted applicant was approved into the group")
		}
		if err := f.approve(f.channel, f.staff, true); err != nil {
			t.Fatalf("approve into broadcast channel with join_group off: %v", err)
		}
		if !f.isMember(f.channel, f.staff) {
			t.Fatal("approved applicant is not a channel member")
		}
	})
	t.Run("join channel off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.channel, f.staff)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinChannel = false })
		assertRPCError(t, "messages.hideChatJoinRequest", f.approve(f.channel, f.staff, true), 403, "USER_PRIVACY_RESTRICTED")
	})
	t.Run("dismissing a restricted applicant is allowed", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.group, f.staff)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		if err := f.approve(f.group, f.staff, false); err != nil {
			t.Fatalf("dismiss restricted applicant: %v", err)
		}
	})
	t.Run("master switch off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.group, f.staff)
		p := domain.DefaultAccountPermissions(f.staff.ID)
		p.Flags.JoinGroup = false
		f.router.deps.Permissions = &fakePermissionsService{items: map[int64]domain.AccountPermissions{f.staff.ID: p}}
		if err := f.approve(f.group, f.staff, true); err != nil {
			t.Fatalf("approve with master switch off: %v", err)
		}
	})
}

func TestPermissionApproveAllJoinRequestsSkipsRestrictedApplicants(t *testing.T) {
	approveAll := func(f *permissionInviteFixture, ch *tg.Channel) {
		f.t.Helper()
		if _, err := f.router.onMessagesHideAllChatJoinRequests(f.userCtx(f.owner), &tg.MessagesHideAllChatJoinRequestsRequest{
			Peer: inputPeerChannel(ch), Approved: true,
		}); err != nil {
			f.t.Fatalf("hideAllChatJoinRequests: %v", err)
		}
	}
	t.Run("join group off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.group, f.staff, f.colleague)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		approveAll(f, f.group)
		if f.isMember(f.group, f.staff) {
			t.Fatal("restricted applicant was approved by hideAllChatJoinRequests")
		}
		if !f.isMember(f.group, f.colleague) {
			t.Fatal("unrestricted applicant was not approved")
		}
	})
	t.Run("join group off does not affect channels", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.channel, f.staff)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		approveAll(f, f.channel)
		if !f.isMember(f.channel, f.staff) {
			t.Fatal("join_group off must not block approval into a broadcast channel")
		}
	})
	t.Run("master switch off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.requestToJoin(f.group, f.staff)
		p := domain.DefaultAccountPermissions(f.staff.ID)
		p.Flags.JoinGroup = false
		f.router.deps.Permissions = &fakePermissionsService{items: map[int64]domain.AccountPermissions{f.staff.ID: p}}
		approveAll(f, f.group)
		if !f.isMember(f.group, f.staff) {
			t.Fatal("master switch off: applicant must be approved as before")
		}
	})
}

func TestPermissionAddChatUserReportsRestrictedInviteeAsMissing(t *testing.T) {
	f := newPermissionInviteFixture(t)
	f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
	res, err := f.router.onMessagesAddChatUser(f.userCtx(f.owner), &tg.MessagesAddChatUserRequest{ChatID: f.group.ID, UserID: inputUser(f.staff)})
	if err != nil {
		t.Fatalf("addChatUser: %v", err)
	}
	assertMissingInvitees(t, res, f.staff.ID)
	if f.isMember(f.group, f.staff) {
		t.Fatal("restricted staff was added through messages.addChatUser")
	}
}

func TestPermissionCreateChatSkipsRestrictedMembers(t *testing.T) {
	createChat := func(f *permissionInviteFixture) (*tg.MessagesInvitedUsers, *tg.Channel) {
		f.t.Helper()
		res, err := f.router.onMessagesCreateChat(f.userCtx(f.owner), &tg.MessagesCreateChatRequest{
			Title: "New group",
			Users: []tg.InputUserClass{inputUser(f.staff), inputUser(f.colleague)},
		})
		if err != nil {
			f.t.Fatalf("createChat: %v", err)
		}
		for _, chat := range res.Updates.(*tg.Updates).Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				return res, ch
			}
		}
		f.t.Fatal("createChat returned no channel")
		return nil, nil
	}
	t.Run("join group off", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		res, ch := createChat(f)
		assertMissingInvitees(t, res, f.staff.ID)
		if f.isMember(ch, f.staff) || !f.isMember(ch, f.colleague) {
			t.Fatal("createChat membership does not match missing_invitees")
		}
	})
	t.Run("join channel off does not affect groups", func(t *testing.T) {
		f := newPermissionInviteFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinChannel = false })
		res, ch := createChat(f)
		assertMissingInvitees(t, res)
		if !f.isMember(ch, f.staff) {
			t.Fatal("join_channel off must not block being added by createChat")
		}
	})
}
