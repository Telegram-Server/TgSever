package domain

import (
	"encoding/json"
	"testing"
)

func TestBotAPIObjectRejectsPrivateOrAmbiguousPayloads(t *testing.T) {
	valid := map[string]any{"id": "query", "from": map[string]any{"id": int64(9007199254740993), "is_bot": false, "first_name": "Caller"}, "query": "search", "offset": ""}
	object, err := NewBotAPIObject("inline_query", valid)
	if err != nil {
		t.Fatal(err)
	}
	body, err := object.Object()
	if err != nil || body["from"].(map[string]any)["id"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("id lost precision: %+v err=%v", body, err)
	}
	for _, key := range []string{"auth_key_id", "session_id", "client_session", "access_hash", "random_id", "bot_token", "origin_device", "payload_hash"} {
		t.Run(key, func(t *testing.T) {
			bad := map[string]any{"id": "query", "from": map[string]any{"id": 1, "is_bot": false, "first_name": "Caller"}, "query": "search", "offset": "", "location": map[string]any{key: "private"}}
			if _, err := NewBotAPIObject("inline_query", bad); err == nil {
				t.Fatal("private nested metadata was retained")
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{"id":"q","from":{"id":1,"is_bot":false,"first_name":"Caller"},"query":"","offset":""} {}`, `{"id":"q","from":{"id":1,"is_bot":false},"query":"","offset":""}`} {
		if err := (&BotAPIObject{Kind: "inline_query", Payload: json.RawMessage(raw)}).Validate(); err == nil {
			t.Fatalf("malformed snapshot accepted: %s", raw)
		}
	}
	if _, err := NewBotAPIObject("arbitrary_private_event", valid); err == nil {
		t.Fatal("unknown kind accepted")
	}
	channel := map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": -1000000000001, "type": "supergroup"}, "text": "group"}
	if _, err := NewBotAPIObject("channel_post", channel); err == nil {
		t.Fatal("supergroup message accepted as channel_post")
	}
}
