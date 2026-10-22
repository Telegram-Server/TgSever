package botapi

import (
	"encoding/json"
	"testing"

	"telesrv/internal/domain"
)

func joinRequestUpdateEvent(link *domain.BotAPIChatInviteLink, bio string) domain.UpdateEvent {
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 555}
	applicant := domain.User{ID: 42, FirstName: "Ann", Username: "ann"}
	return domain.UpdateEvent{
		UserID:         99,
		Type:           domain.UpdateEventBotChatJoinRequest,
		BotAPIUpdateID: 32,
		Date:           1700,
		Peer:           chat,
		Users:          []domain.User{applicant},
		Channels:       []domain.Channel{{ID: 555, Title: "Mods", Username: "modsgroup", Megagroup: true}},
		BotChatJoinRequest: &domain.BotChatJoinRequestUpdate{
			Request:    domain.BotChatJoinRequest{Chat: chat, UserID: applicant.ID, Date: 1700, Bio: bio},
			InviteLink: link,
		},
	}
}

// 官方 Update 的 chat_join_request 字段：ChatJoinRequest{chat, from, user_chat_id, date, bio?, invite_link?}。
func TestAPIUpdateProjectsChatJoinRequestViaInviteLink(t *testing.T) {
	link := &domain.BotAPIChatInviteLink{
		Invite: domain.ChannelInvite{
			ChannelID: 555, Hash: "abc", AdminUserID: 7, Title: "applicants",
			RequestNeeded: true, RequestedCount: 3,
		},
		Link:    "https://t.me/+abc",
		Creator: domain.User{ID: 7, FirstName: "Owner"},
	}
	got, kind, ok := apiUpdate(joinRequestUpdateEvent(link, "likes cats"))
	if !ok || kind != "chat_join_request" {
		t.Fatalf("apiUpdate = %+v, %q, %v", got, kind, ok)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		UpdateID        int64 `json:"update_id"`
		ChatJoinRequest struct {
			Chat struct {
				ID       int64  `json:"id"`
				Type     string `json:"type"`
				Title    string `json:"title"`
				Username string `json:"username"`
			} `json:"chat"`
			From struct {
				ID        int64  `json:"id"`
				FirstName string `json:"first_name"`
				Username  string `json:"username"`
			} `json:"from"`
			UserChatID int64           `json:"user_chat_id"`
			Date       int             `json:"date"`
			Bio        string          `json:"bio"`
			InviteLink json.RawMessage `json:"invite_link"`
		} `json:"chat_join_request"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	body := decoded.ChatJoinRequest
	if decoded.UpdateID != 32 || body.Chat.ID != -1000000000555 || body.Chat.Type != "supergroup" ||
		body.Chat.Title != "Mods" || body.Chat.Username != "modsgroup" {
		t.Fatalf("update = %s", raw)
	}
	if body.From.ID != 42 || body.From.FirstName != "Ann" || body.From.Username != "ann" ||
		body.UserChatID != 42 || body.Date != 1700 || body.Bio != "likes cats" {
		t.Fatalf("request = %s", raw)
	}
	var invite map[string]any
	if err := json.Unmarshal(body.InviteLink, &invite); err != nil {
		t.Fatalf("invite_link = %s: %v", body.InviteLink, err)
	}
	creator, _ := invite["creator"].(map[string]any)
	if invite["invite_link"] != "https://t.me/+abc" || invite["creates_join_request"] != true ||
		invite["name"] != "applicants" || invite["pending_join_request_count"] != float64(3) ||
		creator["id"] != float64(7) {
		t.Fatalf("invite_link = %s", body.InviteLink)
	}
}

// 通过公开用户名申请没有 invite_link；没有 bio 时也省略 bio。
func TestAPIUpdateProjectsChatJoinRequestWithoutLinkOrBio(t *testing.T) {
	got, kind, ok := apiUpdate(joinRequestUpdateEvent(nil, ""))
	if !ok || kind != "chat_join_request" {
		t.Fatalf("apiUpdate = %+v, %q, %v", got, kind, ok)
	}
	body := got["chat_join_request"].(map[string]any)
	for _, field := range []string{"invite_link", "bio"} {
		if _, present := body[field]; present {
			t.Fatalf("absent %s leaked: %+v", field, body)
		}
	}
}

func TestAPIUpdateDropsMalformedChatJoinRequest(t *testing.T) {
	event := joinRequestUpdateEvent(nil, "")
	event.BotChatJoinRequest = nil
	if got, _, ok := apiUpdate(event); ok {
		t.Fatalf("event without payload projected as %+v", got)
	}
}
