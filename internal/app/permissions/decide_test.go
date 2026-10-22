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
