package push

import (
	"testing"
)

// 群组（超级群）推送：会话字段是 channel_id（本服务器没有 basic group），带发送者 chat_from_id，
// 不能出现 from_id（NSE 会把它当成私聊）。
func TestBuildMessagePayloadGroupCarriesChannelIDAndSender(t *testing.T) {
	secret := testSecret()
	m := MessagePush{
		Chat:       Chat{Kind: ChatGroup, ID: 5000001},
		FromUserID: 1000042,
		MessageID:  9,
		Key:        Key("CHAT_MESSAGE_TEXT"),
		Alert:      Alert{Title: "Team", Body: "Alice: hi"},
	}
	payload, err := buildMessagePayload(m, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	inner := decryptPayloadP(t, secret, payload)
	if inner["channel_id"] != "5000001" || inner["chat_from_id"] != "1000042" || inner["msg_id"] != "9" {
		t.Fatalf("inner = %v, want string channel_id, chat_from_id and msg_id", inner)
	}
	if _, ok := inner["from_id"]; ok {
		t.Fatalf("inner = %v, group push must not carry from_id", inner)
	}
	if _, ok := inner["chat_id"]; ok {
		t.Fatalf("inner = %v, supergroups use channel_id, not chat_id", inner)
	}
	aps := inner["aps"].(map[string]any)
	if aps["thread-id"] != "5000001" {
		t.Fatalf("aps = %v, want thread-id of the group", aps)
	}
}

// 频道推送：只有 channel_id，没有发送者字段。
func TestBuildMessagePayloadChannelCarriesOnlyChannelID(t *testing.T) {
	m := MessagePush{
		Chat:       Chat{Kind: ChatChannel, ID: 5000002},
		FromUserID: 1000042,
		MessageID:  3,
		Key:        Key("CHANNEL_MESSAGE_TEXT"),
		Alert:      Alert{Title: "News", Body: "post"},
	}
	payload, err := buildMessagePayload(m, nil, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	outer := decodeJSONObject(t, payload)
	if outer["channel_id"] != "5000002" || outer["msg_id"] != "3" {
		t.Fatalf("payload = %v, want string channel_id and msg_id", outer)
	}
	for _, field := range []string{"from_id", "chat_id", "chat_from_id"} {
		if _, ok := outer[field]; ok {
			t.Fatalf("payload = %v, channel push must not carry %s", outer, field)
		}
	}
}

func decryptPayloadP(t *testing.T, secret, payload []byte) map[string]any {
	t.Helper()
	outer := decodeJSONObject(t, payload)
	p, _ := outer["p"].(string)
	plain, _, err := nseDecrypt(secret, p)
	if err != nil {
		t.Fatalf("decrypt p: %v", err)
	}
	return decodeJSONObject(t, plain)
}
