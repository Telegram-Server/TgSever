package push

import (
	"strings"
	"testing"
)

// oversizedPush 的 title、body 各是渲染上限内最长的 4 字节字符串：加密、base64 之后超过 4KB。
func oversizedPush() MessagePush {
	m := privateTextPush()
	m.Chat = Chat{Kind: ChatGroup, ID: 555}
	m.Key = "CHAT_MESSAGE_TEXT"
	m.Alert = Alert{Title: strings.Repeat("𝄞", maxAlertArgRunes), Body: "Alice: " + strings.Repeat("😀", maxAlertArgRunes)}
	return m
}

func assertShrunkBody(t *testing.T, alert map[string]any, original Alert) {
	t.Helper()
	body, _ := alert["body"].(string)
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("body %q, want truncated with an ellipsis", body)
	}
	if kept := strings.TrimSuffix(body, "…"); kept == "" || !strings.HasPrefix(original.Body, kept) {
		t.Fatalf("body %q, want a non-empty prefix of the original", body)
	}
	if alert["title"] != original.Title {
		t.Fatalf("title changed to %v, want kept while the body can shrink", alert["title"])
	}
}

func TestBuildMessagePayloadEncryptedFitsAPNsLimit(t *testing.T) {
	m := oversizedPush()
	secret := testSecret()
	payload, err := buildMessagePayload(m, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 4096 {
		t.Fatalf("payload is %d bytes, want <= 4096", len(payload))
	}
	inner := decryptPayloadP(t, secret, payload)
	aps, _ := inner["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	assertShrunkBody(t, alert, m.Alert)
}

func TestBuildMessagePayloadPlainFitsAPNsLimit(t *testing.T) {
	m := oversizedPush()
	m.Alert.Body = "Alice: " + strings.Repeat("😀", maxAlertArgRunes) + strings.Repeat("中", maxAlertArgRunes)
	payload, err := buildMessagePayload(m, nil, "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 4096 {
		t.Fatalf("payload is %d bytes, want <= 4096", len(payload))
	}
	aps, _ := decodeJSONObject(t, payload)["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	assertShrunkBody(t, alert, m.Alert)
}

func TestBuildMessagePayloadDoesNotHTMLEscape(t *testing.T) {
	m := privateTextPush()
	m.Alert.Body = "<b> & </b>"
	payload, err := buildMessagePayload(m, nil, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"<b> & </b>"`) {
		t.Fatalf("payload %s, want < > & written literally (\\u003c costs 6 bytes each)", payload)
	}
}

func TestBuildVoIPPayloadRejectsOversizedUpdates(t *testing.T) {
	if _, err := buildVoIPPayload(make([]byte, 4000), testSecret()); err == nil {
		t.Fatal("voip payload over 5120 bytes accepted")
	}
	payload, err := buildVoIPPayload(make([]byte, 1000), testSecret())
	if err != nil || len(payload) > 5120 {
		t.Fatalf("voip payload = %d bytes, %v; want accepted", len(payload), err)
	}
}
