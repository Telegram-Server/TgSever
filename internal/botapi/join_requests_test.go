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

func TestJoinRequestWebhookIncludesStableSnapshot(t *testing.T) {
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 55}
	payload := domain.NewChannelJoinRequest(chat.ID, 42, 1700000000, &domain.ChannelInvite{ChannelID: chat.ID, Hash: "campaign", Link: "https://example.test/+campaign", AdminUserID: 1001, Title: "Autumn", RequestNeeded: true, RequestedCount: 3, Date: 1699999999})
	payload.About = "Please approve"
	event := domain.UpdateEvent{UserID: 1001, BotAPIUpdateID: 31, Type: domain.UpdateEventBotChatJoinRequest, Peer: chat, BotJoinRequest: payload, Users: []domain.User{{ID: 42, FirstName: "Ann"}, {ID: 1001, FirstName: "Admin", Bot: true}}, Channels: []domain.Channel{{ID: 55, Title: "Join Group", Megagroup: true}}}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
		}
		received <- raw
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base := &fakeBotAPIGateway{updates: []domain.UpdateEvent{event}, webhook: domain.BotAPIWebhook{BotUserID: 1001, URL: server.URL, MaxConnections: 1, AllowedUpdates: []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatJoinRequest}}, webhookFound: true}
	gateway := &recordingWebhookGateway{fakeBotAPIGateway: base}
	dispatcher := &webhookDispatcher{control: gateway, gateway: gateway, client: server.Client(), logger: zap.NewNop(), botSem: make(chan struct{}, 1), httpSem: make(chan struct{}, 1)}
	dispatcher.deliver(context.Background(), base.webhook)
	if len(received) != 1 {
		t.Fatalf("webhook requests=%d", len(received))
	}
	var got map[string]any
	if err := json.Unmarshal(<-received, &got); err != nil {
		t.Fatal(err)
	}
	body := got["chat_join_request"].(map[string]any)
	link := body["invite_link"].(map[string]any)
	if got["update_id"] != float64(31) || body["user_chat_id"] != float64(42) || body["bio"] != "Please approve" || body["from"].(map[string]any)["first_name"] != "Ann" || body["chat"].(map[string]any)["title"] != "Join Group" || link["invite_link"] != "https://example.test/+campaign" || link["name"] != "Autumn" || link["pending_join_request_count"] != float64(3) || link["creates_join_request"] != true || link["creator"].(map[string]any)["id"] != float64(1001) {
		t.Fatalf("webhook JSON=%+v", got)
	}
	if base.webhookConfirmed != 31 {
		t.Fatalf("confirmed=%d", base.webhookConfirmed)
	}
}
