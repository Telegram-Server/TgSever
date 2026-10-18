package botapi

import (
	"slices"
	"testing"

	"telesrv/internal/domain"
)

func memberUpdateEvent(eventType domain.UpdateEventType, subject domain.User) domain.UpdateEvent {
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 555}
	actor := domain.User{ID: 7, FirstName: "Owner"}
	return domain.UpdateEvent{
		UserID:         99,
		Type:           eventType,
		BotAPIUpdateID: 31,
		Date:           1700,
		Peer:           chat,
		Users:          []domain.User{actor, subject},
		Channels:       []domain.Channel{{ID: 555, Title: "Mods", Username: "modsgroup", Megagroup: true}},
		BotChatMember: &domain.BotChatMemberUpdated{
			Chat: chat, ActorUserID: actor.ID, Date: 1700, ViaJoinRequest: true,
			Old: domain.BotChatMemberSnapshot{UserID: subject.ID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft},
			New: domain.BotChatMemberSnapshot{
				UserID: subject.ID, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, Rank: "mod",
				AdminRights: domain.ChannelAdminRights{BanUsers: true},
			},
		},
	}
}

func TestAPIUpdateProjectsMyChatMember(t *testing.T) {
	bot := domain.User{ID: 99, FirstName: "GroupBot", Username: "group_bot", Bot: true}
	got, kind, ok := apiUpdate(memberUpdateEvent(domain.UpdateEventBotMyChatMember, bot))
	if !ok || kind != "my_chat_member" || got["update_id"] != int64(31) {
		t.Fatalf("apiUpdate = %+v, %q, %v", got, kind, ok)
	}
	body, ok := got["my_chat_member"].(map[string]any)
	if !ok {
		t.Fatalf("my_chat_member = %+v", got)
	}
	chat := body["chat"].(map[string]any)
	if chat["id"] != int64(-1000000000555) || chat["type"] != "supergroup" || chat["title"] != "Mods" || chat["username"] != "modsgroup" {
		t.Fatalf("chat = %+v", chat)
	}
	from := body["from"].(map[string]any)
	if from["id"] != int64(7) || from["first_name"] != "Owner" {
		t.Fatalf("from = %+v", from)
	}
	if body["date"] != 1700 || body["via_join_request"] != true {
		t.Fatalf("body = %+v", body)
	}
	if _, present := body["via_chat_folder_invite_link"]; present {
		t.Fatalf("unset optional flag leaked: %+v", body)
	}
	old := body["old_chat_member"].(map[string]any)
	updated := body["new_chat_member"].(map[string]any)
	if old["status"] != "left" || updated["status"] != "administrator" || updated["custom_title"] != "mod" ||
		updated["can_restrict_members"] != true {
		t.Fatalf("old = %+v new = %+v", old, updated)
	}
	user := updated["user"].(map[string]any)
	if user["id"] != int64(99) || user["is_bot"] != true || user["username"] != "group_bot" {
		t.Fatalf("member user = %+v", user)
	}
}

func TestAPIUpdateProjectsChatMemberForBroadcastChannel(t *testing.T) {
	event := memberUpdateEvent(domain.UpdateEventBotChatMember, domain.User{ID: 42, FirstName: "Ann"})
	event.Channels = []domain.Channel{{ID: 555, Title: "News", Broadcast: true}}
	got, kind, ok := apiUpdate(event)
	if !ok || kind != "chat_member" {
		t.Fatalf("apiUpdate = %+v, %q, %v", got, kind, ok)
	}
	chat := got["chat_member"].(map[string]any)["chat"].(map[string]any)
	if chat["type"] != "channel" || chat["title"] != "News" {
		t.Fatalf("chat = %+v", chat)
	}
}

func TestAPIUpdateRejectsMemberEventWithoutPayload(t *testing.T) {
	event := memberUpdateEvent(domain.UpdateEventBotChatMember, domain.User{ID: 42})
	event.BotChatMember = nil
	if got, _, ok := apiUpdate(event); ok {
		t.Fatalf("apiUpdate without payload = %+v", got)
	}
}

func TestParseAllowedUpdatesKeepsMemberKinds(t *testing.T) {
	got, err := parseAllowedUpdates(`["message","my_chat_member","chat_member"]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessage, domain.BotAPIUpdateMyChatMember, domain.BotAPIUpdateChatMember}
	if !slices.Equal(got, want) {
		t.Fatalf("allowed = %v, want %v", got, want)
	}
}
