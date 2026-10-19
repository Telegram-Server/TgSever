package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

var memberTestUser = domain.User{ID: 42, FirstName: "Ann", Username: "ann"}

func TestAPIChatMemberStatuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member domain.ChannelMember
		status string
	}{
		{"creator", domain.ChannelMember{Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}, "creator"},
		{"administrator", domain.ChannelMember{Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive}, "administrator"},
		{"member", domain.ChannelMember{Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive}, "member"},
		{"restricted", domain.ChannelMember{Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive,
			BannedRights: domain.ChannelBannedRights{SendPolls: true}}, "restricted"},
		{"left", domain.ChannelMember{Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft}, "left"},
		{"left creator", domain.ChannelMember{Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberLeft}, "left"},
		{"guest", domain.ChannelMember{Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, Guest: true}, "left"},
		{"kicked", domain.ChannelMember{Role: domain.ChannelRoleMember, Status: domain.ChannelMemberKicked,
			BannedRights: domain.ChannelBannedRights{ViewMessages: true}}, "kicked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := apiChatMember(tc.member, memberTestUser, false)
			if got["status"] != tc.status {
				t.Fatalf("status = %v, want %s (all: %+v)", got["status"], tc.status, got)
			}
			user, ok := got["user"].(map[string]any)
			if !ok || user["id"] != int64(42) {
				t.Fatalf("user = %+v, want the member's user object", got["user"])
			}
		})
	}
}

func TestAPIChatMemberAdministratorCarriesRightsAndTitle(t *testing.T) {
	got := apiChatMember(domain.ChannelMember{
		Role:        domain.ChannelRoleAdmin,
		Status:      domain.ChannelMemberActive,
		Rank:        "Moderator",
		AdminRights: domain.ChannelAdminRights{BanUsers: true, PinMessages: true},
	}, memberTestUser, true)
	if got["can_be_edited"] != true || got["custom_title"] != "Moderator" ||
		got["can_restrict_members"] != true || got["can_pin_messages"] != true || got["can_delete_messages"] != false {
		t.Fatalf("administrator = %+v", got)
	}
}

func TestAPIChatMemberCreatorKeepsAnonymityOnly(t *testing.T) {
	got := apiChatMember(domain.ChannelMember{
		Role:        domain.ChannelRoleCreator,
		Status:      domain.ChannelMemberActive,
		AdminRights: domain.ChannelAdminRights{Anonymous: true},
	}, memberTestUser, false)
	if got["is_anonymous"] != true {
		t.Fatalf("creator = %+v, want is_anonymous", got)
	}
	if _, leaked := got["can_delete_messages"]; leaked {
		t.Fatalf("creator = %+v, creator has no per-right flags in the official shape", got)
	}
	if _, titled := got["custom_title"]; titled {
		t.Fatalf("creator = %+v, custom_title must be omitted when empty", got)
	}
}

func TestAPIChatMemberRestrictedCarriesPermissionsAndUntilDate(t *testing.T) {
	got := apiChatMember(domain.ChannelMember{
		Role:         domain.ChannelRoleMember,
		Status:       domain.ChannelMemberActive,
		BannedRights: domain.ChannelBannedRights{SendPolls: true, UntilDate: 1700000000},
	}, memberTestUser, false)
	if got["is_member"] != true || got["until_date"] != 1700000000 ||
		got["can_send_polls"] != false || got["can_send_messages"] != true {
		t.Fatalf("restricted = %+v", got)
	}
}

func TestAPIChatMemberKickedUntilDateZeroMeansForever(t *testing.T) {
	got := apiChatMember(domain.ChannelMember{
		Role:         domain.ChannelRoleMember,
		Status:       domain.ChannelMemberKicked,
		BannedRights: domain.ChannelBannedRights{ViewMessages: true},
	}, memberTestUser, false)
	if got["until_date"] != 0 {
		t.Fatalf("kicked = %+v, want until_date 0", got)
	}
}
