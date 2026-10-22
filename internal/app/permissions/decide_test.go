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
