package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestInlineRichContentSurvivesNativePickerAndUserSend(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := inlineHTTP(t, f)
	if _, err := f.bots.SetInlinePlaceholder(f.ctx, f.bot.ID, "Search"); err != nil {
		t.Fatal(err)
	}
	type response struct {
		result *tg.MessagesBotResults
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := f.router.onMessagesGetInlineBotResults(WithUserID(f.ctx, f.owner.ID), &tg.MessagesGetInlineBotResultsRequest{Bot: inputUser(f.bot), Peer: inputPeerUser(f.member), Query: "rich"})
		done <- response{result, err}
	}()
	queryID := waitInlineBotQuery(t, f.router)
	call("answerInlineQuery", map[string]any{"inline_query_id": strconv.FormatInt(queryID, 10), "results": []any{map[string]any{"type": "article", "id": "rich", "title": "Structured", "input_message_content": map[string]any{"rich_message": map[string]any{"html": "<h3>Structured answer</h3><p>Kept as rich content</p>"}}}}}, true)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		result, ok := got.result.Results[0].(*tg.BotInlineResult)
		if !ok {
			t.Fatalf("picker=%T", got.result.Results[0])
		}
		if _, ok := result.SendMessage.(*tg.BotInlineMessageRichMessage); !ok {
			t.Fatalf("picker message=%T", result.SendMessage)
		}
	case <-time.After(time.Second):
		t.Fatal("rich query unresolved")
	}
	if _, err := f.router.onMessagesSendInlineBotResult(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendInlineBotResultRequest{Peer: inputPeerUser(f.member), QueryID: queryID, ID: "rich", RandomID: 6701}); err != nil {
		t.Fatal(err)
	}
	history, err := f.messages.GetHistory(f.ctx, f.member.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	got := history.Messages[0]
	if got.Body != "" || got.RichMessage.IsZero() || !strings.Contains(string(got.RichMessage.BotAPIProjection), "Structured answer") {
		t.Fatalf("rich send flattened: %+v", got)
	}
}
