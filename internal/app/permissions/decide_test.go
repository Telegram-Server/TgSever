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

func TestDenialErrorNames(t *testing.T) {
	if DenyEdit.Allowed() || !Allowed.Allowed() {
		t.Fatal("Allowed() mismatch")
	}
	if got := string(DenyEdit); got != "TELESRV_PERMISSION_EDIT" {
		t.Fatalf("DenyEdit = %q", got)
	}
}
