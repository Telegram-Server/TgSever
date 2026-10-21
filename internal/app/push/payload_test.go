package push

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("payload %q is not a JSON object: %v", data, err)
	}
	return out
}

func privateTextPush() MessagePush {
	return MessagePush{
		Chat:      Chat{Kind: ChatPrivate, ID: 1000042},
		MessageID: 77,
		Key:       KeyMessageText,
		Alert:     Alert{Title: "Alice", Body: "hello"},
	}
}

func TestBuildMessagePayloadEncryptedCarriesRenderedAlertInsideP(t *testing.T) {
	secret := testSecret()
	payload, err := buildMessagePayload(privateTextPush(), secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "hello") || strings.Contains(string(payload), "Alice") {
		t.Fatalf("outer payload %s leaks message content", payload)
	}
	outer := decodeJSONObject(t, payload)
	aps, _ := outer["aps"].(map[string]any)
	if aps["mutable-content"] != float64(1) {
		t.Fatalf("outer aps = %v, want mutable-content 1", aps)
	}
	if alert, _ := aps["alert"].(map[string]any); alert["body"] != "You have a new message" {
		t.Fatalf("outer alert = %v, want the generic text", aps["alert"])
	}
	p, _ := outer["p"].(string)
	plain, _, err := nseDecrypt(secret, p)
	if err != nil {
		t.Fatalf("decrypt p: %v", err)
	}
	inner := decodeJSONObject(t, plain)
	// iOS NSE 只认字符串值；新消息不能有顶层 loc-key（否则 NSE 置空内容）。
	if inner["from_id"] != "1000042" || inner["msg_id"] != "77" {
		t.Fatalf("inner = %v, want string from_id/msg_id", inner)
	}
	if _, ok := inner["loc-key"]; ok {
		t.Fatalf("inner = %v, must not carry a top-level loc-key", inner)
	}
	if _, ok := inner["silent"]; ok {
		t.Fatalf("inner = %v, want no silent flag", inner)
	}
	innerAPS, _ := inner["aps"].(map[string]any)
	alert, _ := innerAPS["alert"].(map[string]any)
	if alert["title"] != "Alice" || alert["body"] != "hello" || innerAPS["sound"] != "default" {
		t.Fatalf("inner aps = %v, want rendered alert and default sound", innerAPS)
	}
	if innerAPS["thread-id"] != "1000042" {
		t.Fatalf("inner aps = %v, want thread-id of the peer", innerAPS)
	}
}

func TestBuildMessagePayloadPlainWithoutSecret(t *testing.T) {
	payload, err := buildMessagePayload(privateTextPush(), nil, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	outer := decodeJSONObject(t, payload)
	if _, ok := outer["p"]; ok {
		t.Fatalf("payload = %v, want no encrypted field without secret", outer)
	}
	if outer["from_id"] != "1000042" || outer["msg_id"] != "77" {
		t.Fatalf("payload = %v, want string from_id/msg_id", outer)
	}
	aps, _ := outer["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	// Apple 侧 loc-key 解析不到 iOS 的 PUSH_* 文案，不加密路径也发已渲染的 alert。
	if alert["title"] != "Alice" || alert["body"] != "hello" || alert["loc-key"] != nil {
		t.Fatalf("alert = %v, want rendered title/body and no loc-key", alert)
	}
	if aps["mutable-content"] != float64(1) || aps["sound"] != "default" {
		t.Fatalf("aps = %v, want mutable-content 1 and default sound", aps)
	}
}

func TestBuildMessagePayloadSilentOmitsSound(t *testing.T) {
	m := privateTextPush()
	m.Silent = true
	secret := testSecret()
	payload, err := buildMessagePayload(m, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	outer := decodeJSONObject(t, payload)
	if aps, _ := outer["aps"].(map[string]any); aps["sound"] != nil {
		t.Fatalf("outer aps = %v, want no sound for silent", aps)
	}
	plain, _, err := nseDecrypt(secret, outer["p"].(string))
	if err != nil {
		t.Fatal(err)
	}
	inner := decodeJSONObject(t, plain)
	if inner["silent"] != "1" {
		t.Fatalf("inner = %v, want silent \"1\"", inner)
	}
	if aps, _ := inner["aps"].(map[string]any); aps["sound"] != nil {
		t.Fatalf("inner aps = %v, want no sound for silent", aps)
	}
}

func TestBuildMessagePayloadChannelUsesChannelID(t *testing.T) {
	m := privateTextPush()
	m.Chat = Chat{Kind: ChatGroup, ID: 555}
	payload, err := buildMessagePayload(m, nil, "x")
	if err != nil {
		t.Fatal(err)
	}
	outer := decodeJSONObject(t, payload)
	if outer["channel_id"] != "555" || outer["from_id"] != nil || outer["chat_id"] != nil {
		t.Fatalf("payload = %v, want channel_id for a supergroup", outer)
	}
}
