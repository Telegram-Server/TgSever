package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// permissionsWith 返回一个只对 userID 开启总开关、并按 mutate 关掉若干项的权限服务。
func permissionsWith(userID int64, mutate func(*domain.AccountPermissionFlags)) *fakePermissionsService {
	p := domain.DefaultAccountPermissions(userID)
	p.Enabled = true
	mutate(&p.Flags)
	return &fakePermissionsService{items: map[int64]domain.AccountPermissions{userID: p}}
}

type permissionJoinLeaveFixture struct {
	*rpcChannelFixture
	owner, staff domain.User
	group        *tg.Channel
	channel      *tg.Channel
}

func newPermissionJoinLeaveFixture(t *testing.T) *permissionJoinLeaveFixture {
	t.Helper()
	f := &permissionJoinLeaveFixture{rpcChannelFixture: newRPCChannelFixture(t)}
	f.owner = f.user(9101, "15550009101", "Owner")
	f.staff = f.user(9102, "15550009102", "Staff")
	f.group = f.createChannel(&tg.ChannelsCreateChannelRequest{Title: "Work group", Megagroup: true})
	f.channel = f.createChannel(&tg.ChannelsCreateChannelRequest{Title: "Work channel", Broadcast: true})
	return f
}

func (f *permissionJoinLeaveFixture) createChannel(req *tg.ChannelsCreateChannelRequest) *tg.Channel {
	f.t.Helper()
	created, err := f.router.onChannelsCreateChannel(f.userCtx(f.owner), req)
	if err != nil {
		f.t.Fatalf("create %q: %v", req.Title, err)
	}
	return created.(*tg.Updates).Chats[0].(*tg.Channel)
}

func (f *permissionJoinLeaveFixture) restrict(mutate func(*domain.AccountPermissionFlags)) {
	f.router.deps.Permissions = permissionsWith(f.staff.ID, mutate)
}

func (f *permissionJoinLeaveFixture) join(ch *tg.Channel) error {
	_, err := f.router.onChannelsJoinChannel(f.userCtx(f.staff), inputChannel(ch))
	return err
}

func (f *permissionJoinLeaveFixture) inviteHash(ch *tg.Channel, requestNeeded bool) string {
	f.t.Helper()
	req := &tg.MessagesExportChatInviteRequest{Peer: inputPeerChannel(ch), Title: "join"}
	if requestNeeded {
		req.SetRequestNeeded(true)
	}
	invite, err := f.router.onMessagesExportChatInvite(f.userCtx(f.owner), req)
	if err != nil {
		f.t.Fatalf("export invite: %v", err)
	}
	link := invite.(*tg.ChatInviteExported).Link
	return link[strings.LastIndex(link, "+")+1:]
}

func TestPermissionJoinChannelUsesResolvedChatKind(t *testing.T) {
	t.Run("join group off", func(t *testing.T) {
		f := newPermissionJoinLeaveFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		assertPermissionDenied(t, f.join(f.group), "TELESRV_PERMISSION_JOIN_GROUP")
		if err := f.join(f.channel); err != nil {
			t.Fatalf("join broadcast with join_group off: %v", err)
		}
	})
	t.Run("join channel off", func(t *testing.T) {
		f := newPermissionJoinLeaveFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinChannel = false })
		assertPermissionDenied(t, f.join(f.channel), "TELESRV_PERMISSION_JOIN_CHANNEL")
		if err := f.join(f.group); err != nil {
			t.Fatalf("join megagroup with join_channel off: %v", err)
		}
	})
	t.Run("denied join leaves no membership", func(t *testing.T) {
		f := newPermissionJoinLeaveFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		_ = f.join(f.group)
		f.router.deps.Permissions = nil
		if err := f.join(f.group); err != nil {
			t.Fatalf("join after lifting restriction: %v (denied join must not have joined)", err)
		}
	})
}

func TestPermissionImportChatInviteUsesInviteTargetKind(t *testing.T) {
	t.Run("join group off blocks group invite and join request", func(t *testing.T) {
		f := newPermissionJoinLeaveFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		_, err := f.router.onMessagesImportChatInvite(f.userCtx(f.staff), f.inviteHash(f.group, false))
		assertPermissionDenied(t, err, "TELESRV_PERMISSION_JOIN_GROUP")
		_, err = f.router.onMessagesImportChatInvite(f.userCtx(f.staff), f.inviteHash(f.group, true))
		assertPermissionDenied(t, err, "TELESRV_PERMISSION_JOIN_GROUP")
		if _, err := f.router.onMessagesImportChatInvite(f.userCtx(f.staff), f.inviteHash(f.channel, false)); err != nil {
			t.Fatalf("import broadcast invite with join_group off: %v", err)
		}
	})
	t.Run("join channel off blocks channel invite", func(t *testing.T) {
		f := newPermissionJoinLeaveFixture(t)
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinChannel = false })
		_, err := f.router.onMessagesImportChatInvite(f.userCtx(f.staff), f.inviteHash(f.channel, false))
		assertPermissionDenied(t, err, "TELESRV_PERMISSION_JOIN_CHANNEL")
		if _, err := f.router.onMessagesImportChatInvite(f.userCtx(f.staff), f.inviteHash(f.group, false)); err != nil {
			t.Fatalf("import group invite with join_channel off: %v", err)
		}
	})
}

type recordingChatlists struct {
	fakeChatlistsService
	joinInviteCalls, joinUpdatesCalls int
}

func (f *recordingChatlists) JoinInvite(ctx context.Context, userID int64, slug string, peers []domain.DialogFolderPeer, date int) (domain.ChatlistJoinResult, error) {
	f.joinInviteCalls++
	return f.fakeChatlistsService.JoinInvite(ctx, userID, slug, peers, date)
}

func (f *recordingChatlists) JoinUpdates(ctx context.Context, userID int64, filterID int, peers []domain.DialogFolderPeer, date int) (domain.ChatlistJoinResult, error) {
	f.joinUpdatesCalls++
	return f.fakeChatlistsService.JoinUpdates(ctx, userID, filterID, peers, date)
}

// chatlistPeer 是聊天文件夹里的会话：没有 access_hash 的 InputPeerChannel，未加入私有会话的
// 账号也能提交（access_hash 校验需要先能看到会话）。
func chatlistPeer(ch *tg.Channel) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: ch.ID}
}

func TestPermissionChatlistJoinChecksEveryChat(t *testing.T) {
	setup := func(t *testing.T, mutate func(*domain.AccountPermissionFlags)) (*permissionJoinLeaveFixture, *recordingChatlists) {
		f := newPermissionJoinLeaveFixture(t)
		chatlists := &recordingChatlists{}
		chatlists.joinResult.Folder = domain.DialogFolder{ID: 3, Title: "Work", IsChatlist: true}
		chatlists.joinUpdFolder = chatlists.joinResult.Folder
		f.router.deps.Chatlists = chatlists
		f.restrict(mutate)
		return f, chatlists
	}
	both := func(f *permissionJoinLeaveFixture) []tg.InputPeerClass {
		return []tg.InputPeerClass{chatlistPeer(f.channel), chatlistPeer(f.group)}
	}
	joinInvite := func(f *permissionJoinLeaveFixture, peers []tg.InputPeerClass) error {
		_, err := f.router.onChatlistsJoinChatlistInvite(f.userCtx(f.staff), &tg.ChatlistsJoinChatlistInviteRequest{Slug: "work", Peers: peers})
		return err
	}
	joinUpdates := func(f *permissionJoinLeaveFixture, peers []tg.InputPeerClass) error {
		_, err := f.router.onChatlistsJoinChatlistUpdates(f.userCtx(f.staff), &tg.ChatlistsJoinChatlistUpdatesRequest{
			Chatlist: tg.InputChatlistDialogFilter{FilterID: 3}, Peers: peers,
		})
		return err
	}

	t.Run("invite with a forbidden group rejects the whole request", func(t *testing.T) {
		f, chatlists := setup(t, func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		assertPermissionDenied(t, joinInvite(f, both(f)), "TELESRV_PERMISSION_JOIN_GROUP")
		if chatlists.joinInviteCalls != 0 {
			t.Fatalf("JoinInvite called %d times, want none (no partial join)", chatlists.joinInviteCalls)
		}
		if err := joinInvite(f, []tg.InputPeerClass{chatlistPeer(f.channel)}); err != nil || chatlists.joinInviteCalls != 1 {
			t.Fatalf("invite with only a broadcast: err %v calls %d", err, chatlists.joinInviteCalls)
		}
	})
	t.Run("updates with a forbidden channel rejects the whole request", func(t *testing.T) {
		f, chatlists := setup(t, func(p *domain.AccountPermissionFlags) { p.JoinChannel = false })
		assertPermissionDenied(t, joinUpdates(f, both(f)), "TELESRV_PERMISSION_JOIN_CHANNEL")
		if chatlists.joinUpdatesCalls != 0 {
			t.Fatalf("JoinUpdates called %d times, want none", chatlists.joinUpdatesCalls)
		}
		if err := joinUpdates(f, []tg.InputPeerClass{chatlistPeer(f.group)}); err != nil || chatlists.joinUpdatesCalls != 1 {
			t.Fatalf("updates with only a group: err %v calls %d", err, chatlists.joinUpdatesCalls)
		}
	})
	t.Run("chats already joined are not new joins", func(t *testing.T) {
		f, chatlists := setup(t, func(p *domain.AccountPermissionFlags) {})
		if err := f.join(f.group); err != nil {
			t.Fatalf("join group: %v", err)
		}
		f.restrict(func(p *domain.AccountPermissionFlags) { p.JoinGroup = false })
		if err := joinInvite(f, both(f)); err != nil || chatlists.joinInviteCalls != 1 {
			t.Fatalf("invite with an already joined group: err %v calls %d", err, chatlists.joinInviteCalls)
		}
	})
}

type recordingLeaveChatlists struct {
	fakeChatlistsService
	leaveCalls int
}

func (f *recordingLeaveChatlists) Leave(ctx context.Context, userID int64, filterID int, peers []domain.DialogFolderPeer, date int) (domain.ChatlistLeaveResult, error) {
	f.leaveCalls++
	return f.fakeChatlistsService.Leave(ctx, userID, filterID, peers, date)
}

func TestPermissionChatlistLeaveChecksEveryJoinedChat(t *testing.T) {
	// setup 让员工加入 joinGroup/joinChannel 指定的会话，再按 mutate 收紧权限。
	setup := func(t *testing.T, joinGroup, joinChannel bool, mutate func(*domain.AccountPermissionFlags)) (*permissionJoinLeaveFixture, *recordingLeaveChatlists) {
		f := newPermissionJoinLeaveFixture(t)
		for ch, join := range map[*tg.Channel]bool{f.group: joinGroup, f.channel: joinChannel} {
			if !join {
				continue
			}
			if err := f.join(ch); err != nil {
				t.Fatalf("join %q: %v", ch.Title, err)
			}
		}
		chatlists := &recordingLeaveChatlists{}
		f.router.deps.Chatlists = chatlists
		f.restrict(mutate)
		return f, chatlists
	}
	leave := func(f *permissionJoinLeaveFixture, chats ...*tg.Channel) error {
		peers := make([]tg.InputPeerClass, 0, len(chats))
		for _, ch := range chats {
			peers = append(peers, chatlistPeer(ch))
		}
		_, err := f.router.onChatlistsLeaveChatlist(f.userCtx(f.staff), &tg.ChatlistsLeaveChatlistRequest{
			Chatlist: tg.InputChatlistDialogFilter{FilterID: 3}, Peers: peers,
		})
		return err
	}

	t.Run("a forbidden group rejects the whole request", func(t *testing.T) {
		f, chatlists := setup(t, true, true, func(p *domain.AccountPermissionFlags) { p.LeaveGroup = false })
		assertPermissionDenied(t, leave(f, f.channel, f.group), "TELESRV_PERMISSION_LEAVE_GROUP")
		if chatlists.leaveCalls != 0 {
			t.Fatalf("Leave called %d times, want none (no partial leave)", chatlists.leaveCalls)
		}
		if err := leave(f, f.channel); err != nil || chatlists.leaveCalls != 1 {
			t.Fatalf("leave with only a broadcast: err %v calls %d", err, chatlists.leaveCalls)
		}
	})
	t.Run("a forbidden channel rejects the whole request", func(t *testing.T) {
		f, chatlists := setup(t, true, true, func(p *domain.AccountPermissionFlags) { p.LeaveChannel = false })
		assertPermissionDenied(t, leave(f, f.group, f.channel), "TELESRV_PERMISSION_LEAVE_CHANNEL")
		if chatlists.leaveCalls != 0 {
			t.Fatalf("Leave called %d times, want none", chatlists.leaveCalls)
		}
	})
	t.Run("chats the account is not in are not leaves", func(t *testing.T) {
		f, chatlists := setup(t, false, true, func(p *domain.AccountPermissionFlags) { p.LeaveGroup = false })
		if err := leave(f, f.channel, f.group); err != nil || chatlists.leaveCalls != 1 {
			t.Fatalf("leave with a group the account is not in: err %v calls %d", err, chatlists.leaveCalls)
		}
	})
}

func TestPermissionLeaveUsesResolvedChatKind(t *testing.T) {
	setup := func(t *testing.T, mutate func(*domain.AccountPermissionFlags)) *permissionJoinLeaveFixture {
		f := newPermissionJoinLeaveFixture(t)
		if err := f.join(f.group); err != nil {
			t.Fatalf("join group: %v", err)
		}
		if err := f.join(f.channel); err != nil {
			t.Fatalf("join channel: %v", err)
		}
		f.restrict(mutate)
		return f
	}
	leave := func(f *permissionJoinLeaveFixture, ch *tg.Channel) error {
		_, err := f.router.onChannelsLeaveChannel(f.userCtx(f.staff), inputChannel(ch))
		return err
	}
	deleteSelf := func(f *permissionJoinLeaveFixture, ch *tg.Channel) error {
		_, err := f.router.onMessagesDeleteChatUser(f.userCtx(f.staff), &tg.MessagesDeleteChatUserRequest{ChatID: ch.ID, UserID: &tg.InputUserSelf{}})
		return err
	}

	t.Run("leave group off", func(t *testing.T) {
		f := setup(t, func(p *domain.AccountPermissionFlags) { p.LeaveGroup = false })
		assertPermissionDenied(t, leave(f, f.group), "TELESRV_PERMISSION_LEAVE_GROUP")
		assertPermissionDenied(t, deleteSelf(f, f.group), "TELESRV_PERMISSION_LEAVE_GROUP")
		if err := leave(f, f.channel); err != nil {
			t.Fatalf("leave broadcast with leave_group off: %v", err)
		}
	})
	t.Run("leave channel off", func(t *testing.T) {
		f := setup(t, func(p *domain.AccountPermissionFlags) { p.LeaveChannel = false })
		assertPermissionDenied(t, leave(f, f.channel), "TELESRV_PERMISSION_LEAVE_CHANNEL")
		if err := deleteSelf(f, f.group); err != nil {
			t.Fatalf("leave group with leave_channel off: %v", err)
		}
	})
	t.Run("removing someone else is not leaving", func(t *testing.T) {
		f := newPermissionJoinLeaveFixture(t)
		if err := f.join(f.group); err != nil {
			t.Fatalf("join group: %v", err)
		}
		// 群主自己被禁止退出群组，仍可把别人移出群。
		f.router.deps.Permissions = permissionsWith(f.owner.ID, func(p *domain.AccountPermissionFlags) { p.LeaveGroup = false })
		if _, err := f.router.onMessagesDeleteChatUser(f.userCtx(f.owner), &tg.MessagesDeleteChatUserRequest{ChatID: f.group.ID, UserID: inputUser(f.staff)}); err != nil {
			t.Fatalf("remove member with leave_group off: %v", err)
		}
	})
}
