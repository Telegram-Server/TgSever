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

func TestDenialErrorNames(t *testing.T) {
	if DenyEdit.Allowed() || !Allowed.Allowed() {
		t.Fatal("Allowed() mismatch")
	}
	if got := string(DenyEdit); got != "TELESRV_PERMISSION_EDIT" {
		t.Fatalf("DenyEdit = %q", got)
	}
}
