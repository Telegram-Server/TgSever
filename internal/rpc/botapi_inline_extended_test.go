package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	appbots "telesrv/internal/app/bots"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func inlineHTTP(t *testing.T, f botAPIBridgeFixture) func(string, map[string]any, bool) any {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); server.Close() })
	return func(method string, fields map[string]any, want bool) any {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		var result struct {
			OK     bool `json:"ok"`
			Result any  `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.OK != want {
			t.Fatalf("%s = %s err=%v want=%v", method, response.Body.String(), err, want)
		}
		return result.Result
	}
}

func TestInlineQueryHTTPAnswerIsOwnedSingleUseAndSendable(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := inlineHTTP(t, f)
	settings := memory.NewBotGuestQueryStore()
	f.router.deps.BotInlineFeedback = settings
	appbots.WithInlineFeedback(settings)(f.bots)
	if err := f.bots.SetInlineFeedback(f.ctx, f.owner.ID, f.bot.ID, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bots.SetInlinePlaceholder(f.ctx, f.bot.ID, "Search"); err != nil {
		t.Fatal(err)
	}
	type response struct {
		results *tg.MessagesBotResults
		err     error
	}
	done := make(chan response, 1)
	version := f.router.BotAPIUpdateWaitVersion(f.bot.ID)
	go func() {
		got, err := f.router.onMessagesGetInlineBotResults(WithUserID(f.ctx, f.owner.ID), &tg.MessagesGetInlineBotResultsRequest{Bot: inputUser(f.bot), Peer: inputPeerUser(f.member), Query: "weather"})
		done <- response{got, err}
	}()
	queryID := waitInlineBotQuery(t, f.router)
	if !f.router.WaitBotAPIUpdate(f.ctx, f.bot.ID, version, time.Second) {
		t.Fatal("inline query was not delivered to bot queue")
	}
	updates := call("getUpdates", nil, true).([]any)
	found := false
	for _, raw := range updates {
		update := raw.(map[string]any)
		if q, ok := update["inline_query"].(map[string]any); ok {
			if q["id"] != strconv.FormatInt(queryID, 10) || q["query"] != "weather" || q["from"].(map[string]any)["id"] != float64(f.owner.ID) {
				t.Fatalf("query=%v", q)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("missing inline query: %v", updates)
	}
	other, _, err := f.bots.CreateBot(f.ctx, f.owner.ID, "Other", "other_inline_answer_bot")
	if err != nil {
		t.Fatal(err)
	}
	result := domain.BotInlineResult{ID: "foreign", Type: "article", Message: "stolen"}
	if err := f.router.BotAPIAnswerInlineQuery(f.ctx, other.ID, queryID, domain.BotInlineResults{Results: []domain.BotInlineResult{result}}); !tgerr.Is(err, "QUERY_ID_INVALID") {
		t.Fatalf("foreign bot answer=%v", err)
	}
	fields := map[string]any{"inline_query_id": strconv.FormatInt(queryID, 10), "cache_time": 0, "results": []any{map[string]any{"type": "article", "id": "owned", "title": "Weather", "input_message_content": map[string]any{"message_text": "Sunny"}, "reply_markup": map[string]any{"inline_keyboard": []any{[]any{map[string]any{"text": "Refresh", "callback_data": "refresh"}}}}}}}
	call("answerInlineQuery", fields, true)
	call("answerInlineQuery", fields, false)
	select {
	case got := <-done:
		if got.err != nil || len(got.results.Results) != 1 {
			t.Fatalf("native results=%+v err=%v", got.results, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP answer did not resolve native query")
	}
	if _, err := f.router.onMessagesSendInlineBotResult(WithUserID(f.ctx, f.member.ID), &tg.MessagesSendInlineBotResultRequest{Peer: inputPeerUser(f.owner), QueryID: queryID, ID: "owned", RandomID: 4001}); !tgerr.Is(err, "INLINE_RESULT_EXPIRED") {
		t.Fatalf("other user send=%v", err)
	}
	sent, err := f.router.onMessagesSendInlineBotResult(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendInlineBotResultRequest{Peer: inputPeerUser(f.member), QueryID: queryID, ID: "owned", RandomID: 4002})
	if err != nil {
		t.Fatal(err)
	}
	message := messageFromUpdates(t, sent)
	if message.Message != "Sunny" || message.ViaBotID != f.bot.ID {
		t.Fatalf("message=%+v", message)
	}
	feedback := call("getUpdates", nil, true).([]any)
	chosen := false
	for _, raw := range feedback {
		if body, ok := raw.(map[string]any)["chosen_inline_result"].(map[string]any); ok {
			chosen = true
			if body["result_id"] != "owned" || body["query"] != "weather" || body["inline_message_id"] == nil {
				t.Fatalf("chosen=%v", body)
			}
		}
	}
	if !chosen {
		t.Fatalf("missing chosen feedback=%v", feedback)
	}
	expired, _ := f.router.inlines.register(f.router.clock.Now().Add(-botInlineQueryTTL-time.Second), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID})
	if err := f.router.BotAPIAnswerInlineQuery(f.ctx, f.bot.ID, expired, domain.BotInlineResults{Results: []domain.BotInlineResult{result}}); !tgerr.Is(err, "QUERY_ID_INVALID") {
		t.Fatalf("expired answer=%v", err)
	}
}

func TestPreparedKeyboardHTTPUserBindingFiltersAndConsumption(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := inlineHTTP(t, f)
	prepared := call("savePreparedKeyboardButton", map[string]any{"user_id": f.owner.ID, "button": map[string]any{"text": "Share a human", "style": "primary", "request_users": map[string]any{"request_id": 0, "user_is_bot": false, "request_name": true}}}, true).(map[string]any)
	id := prepared["id"].(string)
	if _, err := f.router.onBotsGetRequestedWebViewButton(WithUserID(f.ctx, f.member.ID), &tg.BotsGetRequestedWebViewButtonRequest{Bot: inputUser(f.bot), WebappReqID: id}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("other user read=%v", err)
	}
	button, err := f.router.onBotsGetRequestedWebViewButton(WithUserID(f.ctx, f.owner.ID), &tg.BotsGetRequestedWebViewButtonRequest{Bot: inputUser(f.bot), WebappReqID: id})
	if err != nil {
		t.Fatal(err)
	}
	if button.Text != "Share a human" {
		t.Fatalf("button=%+v", button)
	}
	bad := &tg.MessagesSendBotRequestedPeerRequest{Peer: inputPeerUser(f.bot), ButtonID: 0, RequestedPeers: []tg.InputPeerClass{inputPeerUser(f.bot)}}
	bad.SetWebappReqID(id)
	if _, err := f.router.onMessagesSendBotRequestedPeer(WithUserID(f.ctx, f.owner.ID), bad); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("filter mismatch=%v", err)
	}
	good := &tg.MessagesSendBotRequestedPeerRequest{Peer: inputPeerUser(f.bot), ButtonID: 0, RequestedPeers: []tg.InputPeerClass{inputPeerUser(f.member)}}
	good.SetWebappReqID(id)
	if _, err := f.router.onMessagesSendBotRequestedPeer(WithUserID(f.ctx, f.owner.ID), good); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.onMessagesSendBotRequestedPeer(WithUserID(f.ctx, f.owner.ID), good); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("consumed request=%v", err)
	}
	history, err := f.messages.GetHistory(f.ctx, f.bot.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("bot history=%+v err=%v", history, err)
	}
	shared := history.Messages[0].Media.ServiceAction.RequestedPeer
	if shared.ButtonID != 0 || len(shared.Details) != 1 || shared.Details[0].FirstName != f.member.FirstName {
		t.Fatalf("public sharing snapshot=%+v", shared)
	}
}

func TestGuestQueryRequiresEnabledMentionAndSendsActualBotReply(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	queries := memory.NewBotGuestQueryStore()
	f.router.deps.BotGuestQueries = queries
	appbots.WithGuestQueries(queries)(f.bots)
	call := inlineHTTP(t, f)
	if err := f.bots.SetGuestChat(f.ctx, f.member.ID, f.bot.ID, true); err == nil {
		t.Fatal("non-owner enabled guest mode")
	}
	if err := f.bots.SetGuestChat(f.ctx, f.owner.ID, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendMessageRequest{Peer: inputPeerUser(f.member), RandomID: 5001, Message: "@" + f.bot.Username + " answer this"})
	if err != nil {
		t.Fatal(err)
	}
	updates := call("getUpdates", nil, true).([]any)
	queryID := ""
	for _, raw := range updates {
		if guest, ok := raw.(map[string]any)["guest_message"].(map[string]any); ok {
			queryID = guest["guest_query_id"].(string)
			if guest["text"] != "@"+f.bot.Username+" answer this" {
				t.Fatalf("guest=%v", guest)
			}
		}
	}
	if queryID == "" {
		t.Fatalf("no guest query from real user send: %v", updates)
	}
	fields := map[string]any{"guest_query_id": queryID, "result": map[string]any{"type": "article", "id": "guest", "title": "Answer", "input_message_content": map[string]any{"message_text": "Guest answer"}}}
	result := call("answerGuestQuery", fields, true).(map[string]any)
	if _, ok := result["inline_message_id"].(string); !ok {
		t.Fatalf("guest answer result=%v", result)
	}
	call("answerGuestQuery", fields, false)
	history, err := f.messages.GetHistory(f.ctx, f.member.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	got := history.Messages[0]
	if got.Body != "Guest answer" || got.From != (domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}) || got.GuestChatViaFrom != (domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}) {
		t.Fatalf("guest actual message=%+v", got)
	}
}
