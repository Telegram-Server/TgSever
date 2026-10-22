package permissions

import (
	"testing"

	"telesrv/internal/domain"
)

func enforcedView(mutate func(*domain.AccountPermissions)) View {
	p := domain.DefaultAccountPermissions(1001)
	p.Enabled = true
	if mutate != nil {
		mutate(&p)
	}
	return NewView(p, false)
}

func TestDecideEdit(t *testing.T) {
	editDenied := func(p *domain.AccountPermissions) { p.Flags.Edit = false }
	cases := []struct {
		name   string
		view   View
		action Action
		want   Denial
	}{
		{"no record allows", View{}, Action{Kind: ActionEdit}, Allowed},
		{"master switch off allows even with edit off", NewView(domain.AccountPermissions{UserID: 1001, Enabled: false}, false), Action{Kind: ActionEdit}, Allowed},
		{"enabled with defaults allows", enforcedView(nil), Action{Kind: ActionEdit}, Allowed},
		{"enabled with edit off denies", enforcedView(editDenied), Action{Kind: ActionEdit}, DenyEdit},
		{"edit off denies in saved messages too", enforcedView(editDenied), Action{Kind: ActionEdit, Target: Target{Kind: TargetSelf}}, DenyEdit},
		{"edit off denies in a group", enforcedView(editDenied), Action{Kind: ActionEdit, Target: Target{Kind: TargetGroup}}, DenyEdit},
		{"other items off do not affect edit", enforcedView(func(p *domain.AccountPermissions) {
			p.Flags = domain.AccountPermissionFlags{Edit: true}
			p.SendWhitelistEnabled = true
		}), Action{Kind: ActionEdit}, Allowed},
		{"exempt account allows", NewView(domain.AccountPermissions{UserID: 1001, Enabled: true}, true), Action{Kind: ActionEdit}, Allowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.view, tc.action); got != tc.want {
				t.Fatalf("Decide = %q, want %q", got, tc.want)
			}
		})
	}
}

// allTargets 是判定矩阵里的全部目标种类（含不带目标信息的调用）。
var allTargets = []struct {
	name   string
	target Target
}{
	{"no target", Target{}},
	{"private chat", Target{Kind: TargetPrivate, UserID: 2002}},
	{"group", Target{Kind: TargetGroup}},
	{"channel", Target{Kind: TargetChannel}},
	{"secret chat", Target{Kind: TargetSecretChat, UserID: 2002}},
	{"saved messages", Target{Kind: TargetSelf}},
}

// decideMatrix 对一个动作跑 总开关关/开 × 该项开/关 × 全部目标。turnOff 只关掉被测的那一项；
// savedExempt 为 true 时该项关闭后收藏夹仍放行。
func decideMatrix(t *testing.T, kind ActionKind, turnOff func(*domain.AccountPermissionFlags), want Denial, savedExempt bool) {
	t.Helper()
	itemOff := func(p *domain.AccountPermissions) { turnOff(&p.Flags) }
	onlyItemOn := func(p *domain.AccountPermissions) {
		// 先只关掉这一项，再全部取反：只有这一项允许，其他各项全部禁止。
		f := domain.DefaultAccountPermissionFlags()
		turnOff(&f)
		p.Flags = domain.AccountPermissionFlags{
			Send: !f.Send, Delete: !f.Delete, Edit: !f.Edit, ClearHistory: !f.ClearHistory,
			CreateGroup: !f.CreateGroup, CreateChannel: !f.CreateChannel,
			JoinGroup: !f.JoinGroup, JoinChannel: !f.JoinChannel,
			LeaveGroup: !f.LeaveGroup, LeaveChannel: !f.LeaveChannel,
			AddContact: !f.AddContact,
		}
	}
	masterOff := domain.DefaultAccountPermissions(1001)
	itemOff(&masterOff)
	for _, tc := range allTargets {
		action := Action{Kind: kind, Target: tc.target}
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(NewView(masterOff, false), action); got != Allowed {
				t.Fatalf("master switch off: Decide = %q, want allowed", got)
			}
			if got := Decide(enforcedView(nil), action); got != Allowed {
				t.Fatalf("item allowed: Decide = %q, want allowed", got)
			}
			if got := Decide(enforcedView(onlyItemOn), action); got != Allowed {
				t.Fatalf("only this item allowed: Decide = %q, want allowed", got)
			}
			if got := Decide(NewView(domain.AccountPermissions{UserID: 1001, Enabled: true}, true), action); got != Allowed {
				t.Fatalf("exempt account: Decide = %q, want allowed", got)
			}
			wantDenied := want
			if savedExempt && tc.target.Kind == TargetSelf {
				wantDenied = Allowed
			}
			if got := Decide(enforcedView(itemOff), action); got != wantDenied {
				t.Fatalf("item off: Decide = %q, want %q", got, wantDenied)
			}
		})
	}
}

func TestDecideCreateGroupChannelAndAddContact(t *testing.T) {
	createGroupOff := func(p *domain.AccountPermissions) { p.Flags.CreateGroup = false }
	createChannelOff := func(p *domain.AccountPermissions) { p.Flags.CreateChannel = false }
	addContactOff := func(p *domain.AccountPermissions) { p.Flags.AddContact = false }
	allOff := func(p *domain.AccountPermissions) { p.Flags = domain.AccountPermissionFlags{} }
	cases := []struct {
		name   string
		view   View
		action ActionKind
		want   Denial
	}{
		{"no record allows create group", View{}, ActionCreateGroup, Allowed},
		{"no record allows create channel", View{}, ActionCreateChannel, Allowed},
		{"no record allows add contact", View{}, ActionAddContact, Allowed},
		{"master switch off allows add contact", NewView(domain.AccountPermissions{UserID: 1001}, false), ActionAddContact, Allowed},
		{"exempt account allows create group", NewView(domain.AccountPermissions{UserID: 1001, Enabled: true}, true), ActionCreateGroup, Allowed},
		{"defaults allow create group", enforcedView(nil), ActionCreateGroup, Allowed},
		{"defaults allow create channel", enforcedView(nil), ActionCreateChannel, Allowed},
		{"defaults allow add contact", enforcedView(nil), ActionAddContact, Allowed},
		{"create group off denies create group", enforcedView(createGroupOff), ActionCreateGroup, DenyCreateGroup},
		{"create group off keeps create channel", enforcedView(createGroupOff), ActionCreateChannel, Allowed},
		{"create channel off denies create channel", enforcedView(createChannelOff), ActionCreateChannel, DenyCreateChannel},
		{"create channel off keeps create group", enforcedView(createChannelOff), ActionCreateGroup, Allowed},
		{"add contact off denies add contact", enforcedView(addContactOff), ActionAddContact, DenyAddContact},
		{"add contact off keeps create group", enforcedView(addContactOff), ActionCreateGroup, Allowed},
		{"other items off keep add contact", enforcedView(func(p *domain.AccountPermissions) {
			p.Flags = domain.AccountPermissionFlags{AddContact: true}
		}), ActionAddContact, Allowed},
		{"all off denies create group", enforcedView(allOff), ActionCreateGroup, DenyCreateGroup},
		{"all off denies create channel", enforcedView(allOff), ActionCreateChannel, DenyCreateChannel},
		{"all off denies add contact", enforcedView(allOff), ActionAddContact, DenyAddContact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.view, Action{Kind: tc.action}); got != tc.want {
				t.Fatalf("Decide = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecideSend(t *testing.T) {
	decideMatrix(t, ActionSend, func(f *domain.AccountPermissionFlags) { f.Send = false }, DenySend, true)
}

func TestDecideDelete(t *testing.T) {
	decideMatrix(t, ActionDelete, func(f *domain.AccountPermissionFlags) { f.Delete = false }, DenyDelete, false)
}

func TestDecideClearHistory(t *testing.T) {
	decideMatrix(t, ActionClearHistory, func(f *domain.AccountPermissionFlags) { f.ClearHistory = false }, DenyClearHistory, false)
}

// TestDecideSendWhitelist 覆盖 白名单开/关 × 名单内/外 × 目标种类 × 发送开/关。白名单只看
// 私聊与秘密聊天的对方；群组、频道不看名单，收藏夹始终放行，发送关闭优先于白名单。
func TestDecideSendWhitelist(t *testing.T) {
	const listed, unlisted int64 = 2002, 3003
	targets := []struct {
		name      string
		target    Target
		whitelist bool // 该目标是否受白名单约束
		inList    bool
	}{
		{"private listed", Target{Kind: TargetPrivate, UserID: listed}, true, true},
		{"private unlisted", Target{Kind: TargetPrivate, UserID: unlisted}, true, false},
		{"secret chat listed", Target{Kind: TargetSecretChat, UserID: listed}, true, true},
		{"secret chat unlisted", Target{Kind: TargetSecretChat, UserID: unlisted}, true, false},
		{"secret chat unknown counterpart", Target{Kind: TargetSecretChat}, true, false},
		{"group", Target{Kind: TargetGroup}, false, false},
		{"channel", Target{Kind: TargetChannel}, false, false},
		{"saved messages", Target{Kind: TargetSelf}, false, false},
		{"no target", Target{}, false, false},
	}
	for _, whitelistOn := range []bool{false, true} {
		for _, sendOn := range []bool{true, false} {
			view := enforcedView(func(p *domain.AccountPermissions) {
				p.Flags.Send = sendOn
				p.SendWhitelistEnabled = whitelistOn
				p.SendWhitelist = []int64{listed}
			})
			for _, tc := range targets {
				name := tc.name
				if whitelistOn {
					name += "/whitelist on"
				} else {
					name += "/whitelist off"
				}
				if sendOn {
					name += "/send on"
				} else {
					name += "/send off"
				}
				t.Run(name, func(t *testing.T) {
					want := Allowed
					switch {
					case tc.target.Kind == TargetSelf:
					case !sendOn:
						want = DenySend
					case whitelistOn && tc.whitelist && !tc.inList:
						want = DenySendWhitelist
					}
					if got := Decide(view, Action{Kind: ActionSend, Target: tc.target}); got != want {
						t.Fatalf("Decide = %q, want %q", got, want)
					}
				})
			}
		}
	}
	// 总开关关闭或豁免账号时，白名单开启也不生效。
	off := domain.DefaultAccountPermissions(1001)
	off.SendWhitelistEnabled = true
	if got := Decide(NewView(off, false), Action{Kind: ActionSend, Target: Target{Kind: TargetPrivate, UserID: unlisted}}); got != Allowed {
		t.Fatalf("master switch off: Decide = %q, want allowed", got)
	}
	off.Enabled = true
	if got := Decide(NewView(off, true), Action{Kind: ActionSend, Target: Target{Kind: TargetPrivate, UserID: unlisted}}); got != Allowed {
		t.Fatalf("exempt account: Decide = %q, want allowed", got)
	}
	// 白名单开启但名单为空：所有私聊都被拒绝。
	empty := enforcedView(func(p *domain.AccountPermissions) { p.SendWhitelistEnabled = true; p.SendWhitelist = nil })
	if got := Decide(empty, Action{Kind: ActionSend, Target: Target{Kind: TargetPrivate, UserID: listed}}); got != DenySendWhitelist {
		t.Fatalf("empty whitelist: Decide = %q, want %q", got, DenySendWhitelist)
	}
}

func TestDenialErrorNames(t *testing.T) {
	if DenyEdit.Allowed() || !Allowed.Allowed() {
		t.Fatal("Allowed() mismatch")
	}
	if got := string(DenyEdit); got != "TELESRV_PERMISSION_EDIT" {
		t.Fatalf("DenyEdit = %q", got)
	}
}

func TestChannelTargetClassifiesGroupsAndChannels(t *testing.T) {
	cases := []struct {
		name    string
		channel domain.Channel
		want    TargetKind
	}{
		{"megagroup is a group", domain.Channel{Megagroup: true}, TargetGroup},
		{"forum is a group", domain.Channel{Megagroup: true, Forum: true}, TargetGroup},
		{"gigagroup is a group", domain.Channel{Gigagroup: true}, TargetGroup},
		{"monoforum is a group", domain.Channel{Monoforum: true}, TargetGroup},
		{"legacy chat (no flags) is a group", domain.Channel{}, TargetGroup},
		{"broadcast is a channel", domain.Channel{Broadcast: true}, TargetChannel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChannelTarget(tc.channel)
			if got.Kind != tc.want || got.UserID != 0 {
				t.Fatalf("ChannelTarget = %+v, want kind %d", got, tc.want)
			}
		})
	}
}

func TestDecideJoinAndLeave(t *testing.T) {
	type flagSet func(*domain.AccountPermissionFlags)
	items := []struct {
		name   string
		kind   ActionKind
		target TargetKind
		off    flagSet
		deny   Denial
	}{
		{"join group", ActionJoin, TargetGroup, func(f *domain.AccountPermissionFlags) { f.JoinGroup = false }, DenyJoinGroup},
		{"join channel", ActionJoin, TargetChannel, func(f *domain.AccountPermissionFlags) { f.JoinChannel = false }, DenyJoinChannel},
		{"leave group", ActionLeave, TargetGroup, func(f *domain.AccountPermissionFlags) { f.LeaveGroup = false }, DenyLeaveGroup},
		{"leave channel", ActionLeave, TargetChannel, func(f *domain.AccountPermissionFlags) { f.LeaveChannel = false }, DenyLeaveChannel},
	}
	other := map[TargetKind]TargetKind{TargetGroup: TargetChannel, TargetChannel: TargetGroup}
	for _, item := range items {
		t.Run(item.name, func(t *testing.T) {
			off := enforcedView(func(p *domain.AccountPermissions) { item.off(&p.Flags) })
			action := Action{Kind: item.kind, Target: Target{Kind: item.target}}
			if got := Decide(off, action); got != item.deny {
				t.Fatalf("item off: Decide = %q, want %q", got, item.deny)
			}
			if got := Decide(enforcedView(nil), action); got != Allowed {
				t.Fatalf("item on: Decide = %q, want allowed", got)
			}
			// 总开关关闭时同样的设置不生效。
			disabled := domain.DefaultAccountPermissions(1001)
			item.off(&disabled.Flags)
			if got := Decide(NewView(disabled, false), action); got != Allowed {
				t.Fatalf("master switch off: Decide = %q, want allowed", got)
			}
			// 只关掉本项不影响另一种会话。
			if got := Decide(off, Action{Kind: item.kind, Target: Target{Kind: other[item.target]}}); got != Allowed {
				t.Fatalf("other target kind: Decide = %q, want allowed", got)
			}
			// 加入与退出互不影响。
			otherKind := ActionLeave
			if item.kind == ActionLeave {
				otherKind = ActionJoin
			}
			if got := Decide(off, Action{Kind: otherKind, Target: Target{Kind: item.target}}); got != Allowed {
				t.Fatalf("other action: Decide = %q, want allowed", got)
			}
		})
	}
	t.Run("all off covers every chat kind", func(t *testing.T) {
		v := enforcedView(func(p *domain.AccountPermissions) { p.Flags = domain.AccountPermissionFlags{} })
		for _, tc := range []struct {
			a    Action
			want Denial
		}{
			{Action{Kind: ActionJoin, Target: ChannelTarget(domain.Channel{Megagroup: true, Forum: true})}, DenyJoinGroup},
			{Action{Kind: ActionJoin, Target: ChannelTarget(domain.Channel{Gigagroup: true})}, DenyJoinGroup},
			{Action{Kind: ActionJoin, Target: ChannelTarget(domain.Channel{Broadcast: true})}, DenyJoinChannel},
			{Action{Kind: ActionLeave, Target: ChannelTarget(domain.Channel{Megagroup: true})}, DenyLeaveGroup},
			{Action{Kind: ActionLeave, Target: ChannelTarget(domain.Channel{Broadcast: true})}, DenyLeaveChannel},
		} {
			if got := Decide(v, tc.a); got != tc.want {
				t.Fatalf("Decide(%+v) = %q, want %q", tc.a, got, tc.want)
			}
		}
	})
	t.Run("exempt account allows", func(t *testing.T) {
		p := domain.DefaultAccountPermissions(1001)
		p.Enabled = true
		p.Flags = domain.AccountPermissionFlags{}
		if got := Decide(NewView(p, true), Action{Kind: ActionJoin, Target: Target{Kind: TargetGroup}}); got != Allowed {
			t.Fatalf("Decide = %q", got)
		}
	})
}
