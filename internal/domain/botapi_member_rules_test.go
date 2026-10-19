package domain

import (
	"encoding/json"
	"testing"
)

// bot_api_updates.member_payload 的旧行没有 viewer 字段，必须能正常反序列化为零值。
func TestBotChatMemberUpdatedDecodesLegacyPayloadWithoutViewer(t *testing.T) {
	raw := `{"chat":{"type":"channel","id":5},"actor_user_id":7,"date":10,` +
		`"old":{"user_id":42,"role":"member","status":"left","admin_rights":{},"banned_rights":{}},` +
		`"new":{"user_id":42,"role":"member","status":"active","admin_rights":{},"banned_rights":{}}}`
	var got BotChatMemberUpdated
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode legacy payload: %v", err)
	}
	if got.Viewer != (BotChatMemberSnapshot{}) || got.New.Status != ChannelMemberActive {
		t.Fatalf("legacy payload = %+v", got)
	}
	if err := got.ValidateFor(99, BotAPIUpdateChatMember); err != nil {
		t.Fatalf("legacy payload invalid: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	_ = json.Unmarshal(encoded, &back)
	if _, present := back["viewer"]; present {
		t.Fatalf("zero viewer serialized: %s", encoded)
	}
}
