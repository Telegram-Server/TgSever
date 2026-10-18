package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

// chat_member 的 can_be_edited 按收件机器人的视角算：它有 add_admins 且是该管理员的提拔人。
func TestAPIUpdateChatMemberCanBeEditedFollowsRecipientBot(t *testing.T) {
	project := func(event domain.UpdateEvent) map[string]any {
		t.Helper()
		got, kind, ok := apiUpdate(event)
		if !ok || kind != "chat_member" {
			t.Fatalf("apiUpdate = %+v, %q, %v", got, kind, ok)
		}
		return got["chat_member"].(map[string]any)["new_chat_member"].(map[string]any)
	}
	event := memberUpdateEvent(domain.UpdateEventBotChatMember, domain.User{ID: 42, FirstName: "Ann"})
	event.BotChatMember.New.InviterUserID = 99
	event.BotChatMember.Viewer = domain.BotChatMemberSnapshot{
		UserID: 99, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive,
		AdminRights: domain.ChannelAdminRights{AddAdmins: true},
	}
	if got := project(event); got["can_be_edited"] != true {
		t.Fatalf("promoted by the recipient bot: %+v", got)
	}

	other := memberUpdateEvent(domain.UpdateEventBotChatMember, domain.User{ID: 42, FirstName: "Ann"})
	other.BotChatMember.New.InviterUserID = 7
	other.BotChatMember.Viewer = event.BotChatMember.Viewer
	if got := project(other); got["can_be_edited"] != false {
		t.Fatalf("promoted by someone else: %+v", got)
	}

	// 旧行没有 viewer：保持 false。
	legacy := memberUpdateEvent(domain.UpdateEventBotChatMember, domain.User{ID: 42, FirstName: "Ann"})
	legacy.BotChatMember.New.InviterUserID = 99
	if got := project(legacy); got["can_be_edited"] != false {
		t.Fatalf("legacy row without viewer: %+v", got)
	}
}
