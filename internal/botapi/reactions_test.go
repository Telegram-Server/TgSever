package botapi

import (
	"context"
	"encoding/json"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"telesrv/internal/domain"
	"testing"
)

func reactionProjectionEvents() []domain.UpdateEvent {
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 55}
	old := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	next := domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 12345678901234567}
	payload := &domain.BotMessageReactionUpdated{Chat: chat, Date: 1700000000, MessageReactionChange: domain.MessageReactionChange{MessageID: 9, ActorUserID: 42, OldReactions: []domain.MessageReaction{old}, NewReactions: []domain.MessageReaction{next}}}
	identified := domain.UpdateEvent{UserID: 1001, BotAPIUpdateID: 31, Type: domain.UpdateEventBotMessageReaction, Peer: chat, BotReaction: payload, Users: []domain.User{{ID: 42, FirstName: "Ann"}}, Channels: []domain.Channel{{ID: 55, Title: "Reactions", Megagroup: true}}}
	anonymous := identified
	anonymous.BotAPIUpdateID, anonymous.Type = 32, domain.UpdateEventBotMessageReactionCount
	anonymous.BotReaction = payload.Clone()
	anonymous.BotReaction.Counts = []domain.ChannelMessageReactionCount{{Reaction: next, Count: 5}}
	anonymous.Channels = []domain.Channel{{ID: 55, Title: "Reactions", Broadcast: true}}
	return []domain.UpdateEvent{identified, anonymous}
}

func assertReactionJSON(t *testing.T, raw []byte, count bool) {
	t.Helper()
	var update map[string]any
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	kind := "message_reaction"
	if count {
		kind = "message_reaction_count"
	}
	body, ok := update[kind].(map[string]any)
	if !ok || body["message_id"] != float64(9) || body["date"] != float64(1700000000) {
		t.Fatalf("reaction JSON=%s", raw)
	}
	chat := body["chat"].(map[string]any)
	if chat["id"] != float64(-1000000000055) || chat["title"] != "Reactions" {
		t.Fatalf("chat JSON=%s", raw)
	}
	if count {
		reactions := body["reactions"].([]any)
		entry := reactions[0].(map[string]any)
		if update["update_id"] != float64(32) || chat["type"] != "channel" || entry["total_count"] != float64(5) || entry["type"].(map[string]any)["custom_emoji_id"] != "12345678901234567" {
			t.Fatalf("count JSON=%s", raw)
		}
		if _, exists := body["user"]; exists {
			t.Fatalf("anonymous update leaks actor: %s", raw)
		}
		if _, exists := body["old_reaction"]; exists {
			t.Fatalf("anonymous update leaks selection: %s", raw)
		}
	} else {
		if update["update_id"] != float64(31) || chat["type"] != "supergroup" || body["user"].(map[string]any)["id"] != float64(42) || body["old_reaction"].([]any)[0].(map[string]any)["emoji"] != "👍" || body["new_reaction"].([]any)[0].(map[string]any)["custom_emoji_id"] != "12345678901234567" {
			t.Fatalf("identified JSON=%s", raw)
		}
	}
}

func TestReactionGetUpdatesProjectsOfficialShapes(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{updates: reactionProjectionEvents()})
	rec := callMethod(t, h, "getUpdates", `{"allowed_updates":["message_reaction","message_reaction_count"]}`)
	var response struct {
		OK     bool              `json:"ok"`
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.OK || len(response.Result) != 2 {
		t.Fatalf("getUpdates=%s err=%v", rec.Body.String(), err)
	}
	assertReactionJSON(t, response.Result[0], false)
	assertReactionJSON(t, response.Result[1], true)
}

func TestReactionWebhookUsesSameOfficialProjection(t *testing.T) {
	received := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base := &fakeBotAPIGateway{updates: reactionProjectionEvents(), webhook: domain.BotAPIWebhook{BotUserID: 1001, URL: server.URL, MaxConnections: 2}, webhookFound: true}
	gateway := &recordingWebhookGateway{fakeBotAPIGateway: base}
	d := &webhookDispatcher{control: gateway, gateway: gateway, client: server.Client(), logger: zap.NewNop(), botSem: make(chan struct{}, 1), httpSem: make(chan struct{}, 2)}
	d.deliver(context.Background(), base.webhook)
	if len(received) != 2 {
		t.Fatalf("received %d reaction updates", len(received))
	}
	for range 2 {
		raw := <-received
		var update map[string]any
		if err := json.Unmarshal(raw, &update); err != nil {
			t.Fatal(err)
		}
		assertReactionJSON(t, raw, update["message_reaction_count"] != nil)
	}
	if base.webhookConfirmed != 32 {
		t.Fatalf("confirmed=%d", base.webhookConfirmed)
	}
}
