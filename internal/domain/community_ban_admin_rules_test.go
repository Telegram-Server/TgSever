package domain

import (
	"errors"
	"testing"
)

func TestCheckManageChannelAdmin(t *testing.T) {
	const owner, promoter, other, target = 1, 2, 3, 4
	admin := ChannelMember{UserID: target, Role: ChannelRoleAdmin, Status: ChannelMemberActive, InviterUserID: promoter}
	cases := []struct {
		name   string
		actor  ChannelMember
		target ChannelMember
		want   error
	}{
		{"creator may manage any admin", ChannelMember{UserID: owner, Role: ChannelRoleCreator, Status: ChannelMemberActive}, admin, nil},
		{"promoter may manage own promotee", ChannelMember{UserID: promoter, Role: ChannelRoleAdmin, Status: ChannelMemberActive}, admin, nil},
		{"other admin may not", ChannelMember{UserID: other, Role: ChannelRoleAdmin, Status: ChannelMemberActive}, admin, ErrUserAdminInvalid},
		{"non-member actor may not", ChannelMember{UserID: other}, admin, ErrUserAdminInvalid},
		{"self is exempt", ChannelMember{UserID: target, Role: ChannelRoleAdmin, Status: ChannelMemberActive}, admin, nil},
		{"plain member target", ChannelMember{UserID: other}, ChannelMember{UserID: target, Role: ChannelRoleMember, Status: ChannelMemberActive}, nil},
		{"departed admin target", ChannelMember{UserID: other}, ChannelMember{UserID: target, Role: ChannelRoleAdmin, Status: ChannelMemberLeft, InviterUserID: promoter}, nil},
	}
	for _, tc := range cases {
		if got := CheckManageChannelAdmin(tc.actor, tc.target); !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
			t.Errorf("%s: CheckManageChannelAdmin = %v, want %v", tc.name, got, tc.want)
		}
	}
}
