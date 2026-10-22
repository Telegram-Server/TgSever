package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
	"telesrv/internal/domain"
)

func TestInviteSecretsAreRecipientScopedOverHTTPAndWebhook(t *testing.T) {
	const botID int64 = 1001
	const secret = "PRIVATE-CAMPAIGN-HASH"
	chat := domain.Peer{Type: domain.PeerTypeChannel, ID: 55}
	for _, kind := range []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatJoinRequest, domain.BotAPIUpdateChatMember} {
		for _, tc := range []struct {
			name       string
			creator    int64
			link, want string
		}{
			{"own custom link", botID, "https://example.test/gram/+" + secret, "https://example.test/gram/+" + secret},
			{"foreign custom link", 2002, "https://example.test/gram/+" + secret, "https://example.test/gram/+..."},
			{"foreign legacy link", 2002, "https://t.me/joinchat/" + secret, "https://t.me/joinchat/..."},
			{"foreign fallback link", 2002, "", "https://t.me/+..."},
		} {
			t.Run(fmt.Sprintf("%s/%s", kind, tc.name), func(t *testing.T) {
				invite := domain.ChannelInvite{ChannelID: chat.ID, Hash: secret, Link: tc.link, AdminUserID: tc.creator, Title: "Campaign", RequestNeeded: true, RequestedCount: 3, Date: 1699999999}
				event := domain.UpdateEvent{UserID: botID, BotAPIUpdateID: 31, Peer: chat, Date: 1700000000, Users: []domain.User{{ID: 42, FirstName: "Applicant"}, {ID: tc.creator, FirstName: "Creator", Bot: tc.creator == botID}}, Channels: []domain.Channel{{ID: 55, Title: "Join group", Megagroup: true}}}
				if kind == domain.BotAPIUpdateChatJoinRequest {
					event.Type = domain.UpdateEventBotChatJoinRequest
					event.BotJoinRequest = domain.NewChannelJoinRequest(chat.ID, 42, event.Date, &invite)
				} else {
					event.Type = domain.UpdateEventBotChatMember
					event.BotChatMember = &domain.BotChatMemberUpdated{Chat: chat, ActorUserID: tc.creator, Date: event.Date, Old: domain.BotChatMemberSnapshot{UserID: 42, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft}, New: domain.BotChatMemberSnapshot{UserID: 42, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive}, Viewer: domain.BotChatMemberSnapshot{UserID: botID, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, AdminRights: domain.ChannelAdminRights{InviteUsers: true}}, Invite: &invite}
				}
				check := func(raw []byte, update map[string]any) {
					t.Helper()
					body, ok := update[string(kind)].(map[string]any)
					if !ok {
						t.Fatalf("missing %s: %+v", kind, update)
					}
					link, ok := body["invite_link"].(map[string]any)
					if !ok {
						t.Fatalf("missing invite metadata: %+v", body)
					}
					creator := link["creator"].(map[string]any)
					if link["invite_link"] != tc.want || link["name"] != "Campaign" || link["creates_join_request"] != true || creator["id"] != float64(tc.creator) {
						t.Fatalf("invite JSON=%+v, want link %q creator %d", link, tc.want, tc.creator)
					}
					if tc.creator != botID && bytes.Contains(raw, []byte(secret)) {
						t.Fatal("foreign private hash leaked in serialized response")
					}
				}
				base := &fakeBotAPIGateway{updates: []domain.UpdateEvent{event}}
				rec := callMethod(t, newTestHandler(t, base), "getUpdates", `{}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("getUpdates status=%d body=%s", rec.Code, rec.Body.String())
				}
				response := decodeAPIResponse(t, rec)
				var updates []map[string]any
				if err := json.Unmarshal(response.Result, &updates); err != nil {
					t.Fatal(err)
				}
				if len(updates) != 1 {
					t.Fatalf("getUpdates=%s", response.Result)
				}
				check(rec.Body.Bytes(), updates[0])

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
				base.webhook = domain.BotAPIWebhook{BotUserID: botID, URL: server.URL, MaxConnections: 1}
				base.webhookFound = true
				gateway := &recordingWebhookGateway{fakeBotAPIGateway: base}
				dispatcher := &webhookDispatcher{control: gateway, gateway: gateway, client: server.Client(), logger: zap.NewNop(), botSem: make(chan struct{}, 1), httpSem: make(chan struct{}, 1)}
				dispatcher.deliver(context.Background(), base.webhook)
				select {
				case raw := <-received:
					var update map[string]any
					if err := json.Unmarshal(raw, &update); err != nil {
						t.Fatal(err)
					}
					check(raw, update)
				case <-time.After(2 * time.Second):
					t.Fatal("no webhook update")
				}
			})
		}
	}
}
