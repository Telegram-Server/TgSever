package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

// chat_member 的 can_be_edited 按收件机器人（载荷里的 viewer）的视角算。规则本身（提拔人、
// 群主、离开等）由 domain.TestCanEditChannelAdmin 覆盖；这里只证明 viewer 被交给了投影。
func TestAPIUpdateChatMemberCanBeEditedFollowsRecipientBot(t *testing.T) {
	event := memberUpdateEvent(domain.UpdateEventBotChatMember, domain.User{ID: 42, FirstName: "Ann"})
	event.BotChatMember.New.InviterUserID = 99
	event.BotChatMember.Viewer = domain.BotChatMemberSnapshot{
		UserID: 99, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive,
		AdminRights: domain.ChannelAdminRights{AddAdmins: true},
	}
	got, kind, ok := apiUpdate(event)
	if !ok || kind != "chat_member" {
		t.Fatalf("apiUpdate = %+v, %q, %v", got, kind, ok)
	}
	if member := got["chat_member"].(map[string]any)["new_chat_member"].(map[string]any); member["can_be_edited"] != true {
		t.Fatalf("promoted by the recipient bot: %+v", member)
	}
}
