package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

// fakeForwardGateway 记录转发/复制调用，并按调用返回预置结果。
type fakeForwardGateway struct {
	*fakeBotAPIGateway

	forwardCalls []forwardCall
	copyCalls    []forwardCall
	result       []domain.Message
	err          error
}

type forwardCall struct {
	botID, chatID, fromChatID int64
	messageIDs                []int
	silent, protect           bool
	removeCaption             bool
	setCaption                bool
	caption                   string
	entities                  []domain.MessageEntity
	markup                    *domain.MessageReplyMarkup
	replyTo                   int
}

func newFakeForwardGateway() *fakeForwardGateway {
	return &fakeForwardGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (g *fakeForwardGateway) BotAPIForwardMessages(_ context.Context, botID, chatID, fromChatID int64, messageIDs []int, silent, protect bool) ([]domain.Message, error) {
	g.forwardCalls = append(g.forwardCalls, forwardCall{botID: botID, chatID: chatID, fromChatID: fromChatID,
		messageIDs: append([]int(nil), messageIDs...), silent: silent, protect: protect})
	return g.result, g.err
}

func (g *fakeForwardGateway) BotAPICopyMessages(_ context.Context, botID, chatID, fromChatID int64, messageIDs []int, removeCaption, silent, protect bool) ([]domain.Message, error) {
	g.copyCalls = append(g.copyCalls, forwardCall{botID: botID, chatID: chatID, fromChatID: fromChatID,
		messageIDs: append([]int(nil), messageIDs...), removeCaption: removeCaption, silent: silent, protect: protect})
	return g.result, g.err
}

func (g *fakeForwardGateway) BotAPICopyMessageWithOptions(_ context.Context, botID, chatID, fromChatID int64, messageID int, setCaption bool, caption string, entities []domain.MessageEntity, markup *domain.MessageReplyMarkup, silent, protect bool, replyTo int, _ domain.BotAPIMessageOptions) (domain.Message, error) {
	g.copyCalls = append(g.copyCalls, forwardCall{botID: botID, chatID: chatID, fromChatID: fromChatID,
		messageIDs: []int{messageID}, setCaption: setCaption, caption: caption, entities: entities, markup: markup,
		silent: silent, protect: protect, replyTo: replyTo})
	if g.err != nil || len(g.result) == 0 {
		return domain.Message{}, g.err
	}
	return g.result[0], nil
}

func TestForwardMessagePassesParamsAndReturnsMessage(t *testing.T) {
	g := newFakeForwardGateway()
	g.result = []domain.Message{{
		ID: 77, Date: 5, OwnerUserID: testBotProfile.BotUserID, Out: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: 42},
		From:    domain.Peer{Type: domain.PeerTypeUser, ID: testBotProfile.BotUserID},
		Body:    "hi",
		Forward: &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: 9}, Date: 3},
	}}
	h := newTestHandler(t, g)

	rec := callMethod(t, h, "forwardMessage", `{"chat_id":42,"from_chat_id":-1000000000123,"message_id":7,"disable_notification":true,"protect_content":true}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("forwardMessage = %d %s", rec.Code, rec.Body.String())
	}
	if len(g.forwardCalls) != 1 {
		t.Fatalf("forward calls = %+v", g.forwardCalls)
	}
	call := g.forwardCalls[0]
	if call.botID != testBotProfile.BotUserID || call.chatID != 42 || call.fromChatID != -1000000000123 ||
		len(call.messageIDs) != 1 || call.messageIDs[0] != 7 || !call.silent || !call.protect {
		t.Fatalf("call = %+v", call)
	}
	var msg map[string]any
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if msg["message_id"] != float64(77) || msg["text"] != "hi" {
		t.Fatalf("message = %+v", msg)
	}
	origin, ok := msg["forward_origin"].(map[string]any)
	if !ok || origin["type"] != "user" || origin["date"] != float64(3) {
		t.Fatalf("forward_origin = %+v", msg["forward_origin"])
	}
	if user, ok := origin["sender_user"].(map[string]any); !ok || user["id"] != float64(9) {
		t.Fatalf("sender_user = %+v", origin["sender_user"])
	}
}

func TestForwardMessagesSortsIDsAndReturnsMessageIDs(t *testing.T) {
	g := newFakeForwardGateway()
	g.result = []domain.Message{{ID: 11}, {ID: 12}}
	h := newTestHandler(t, g)

	rec := callMethod(t, h, "forwardMessages", `{"chat_id":42,"from_chat_id":43,"message_ids":[9,3,9,5]}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("forwardMessages = %d %s", rec.Code, rec.Body.String())
	}
	if len(g.forwardCalls) != 1 || len(g.forwardCalls[0].messageIDs) != 3 ||
		g.forwardCalls[0].messageIDs[0] != 3 || g.forwardCalls[0].messageIDs[1] != 5 || g.forwardCalls[0].messageIDs[2] != 9 {
		t.Fatalf("forward calls = %+v, want ids [3 5 9]", g.forwardCalls)
	}
	if string(resp.Result) != `[{"message_id":11},{"message_id":12}]` {
		t.Fatalf("result = %s", resp.Result)
	}
}

func TestCopyMessagesPassesRemoveCaption(t *testing.T) {
	g := newFakeForwardGateway()
	g.result = []domain.Message{{ID: 21}}
	h := newTestHandler(t, g)

	rec := callMethod(t, h, "copyMessages", `{"chat_id":42,"from_chat_id":43,"message_ids":[4],"remove_caption":true,"disable_notification":true}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("copyMessages = %d %s", rec.Code, rec.Body.String())
	}
	if len(g.forwardCalls) != 0 || len(g.copyCalls) != 1 {
		t.Fatalf("forward=%+v copy=%+v", g.forwardCalls, g.copyCalls)
	}
	call := g.copyCalls[0]
	if call.chatID != 42 || call.fromChatID != 43 || len(call.messageIDs) != 1 || call.messageIDs[0] != 4 ||
		!call.removeCaption || !call.silent || call.protect {
		t.Fatalf("call = %+v", call)
	}
	if string(resp.Result) != `[{"message_id":21}]` {
		t.Fatalf("result = %s", resp.Result)
	}
}

func TestCopyMessageReplacesCaptionAndReturnsMessageID(t *testing.T) {
	g := newFakeForwardGateway()
	g.result = []domain.Message{{ID: 31}}
	h := newTestHandler(t, g)

	rec := callMethod(t, h, "copyMessage", `{"chat_id":42,"from_chat_id":43,"message_id":6,"caption":"<b>new</b>","parse_mode":"HTML",`+
		`"reply_markup":{"inline_keyboard":[[{"text":"go","callback_data":"x"}]]},"reply_parameters":{"message_id":2},"protect_content":true}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("copyMessage = %d %s", rec.Code, rec.Body.String())
	}
	if string(resp.Result) != `{"message_id":31}` {
		t.Fatalf("result = %s", resp.Result)
	}
	if len(g.copyCalls) != 1 {
		t.Fatalf("copy calls = %+v", g.copyCalls)
	}
	call := g.copyCalls[0]
	if call.chatID != 42 || call.fromChatID != 43 || call.messageIDs[0] != 6 || !call.setCaption || call.caption != "new" ||
		len(call.entities) != 1 || call.entities[0].Type != domain.MessageEntityBold || call.replyTo != 2 || !call.protect || call.silent {
		t.Fatalf("call = %+v", call)
	}
	if call.markup == nil || call.markup.Kind() != domain.MessageReplyMarkupInline {
		t.Fatalf("markup = %+v", call.markup)
	}
}

func TestCopyMessageWithoutCaptionKeepsOriginal(t *testing.T) {
	g := newFakeForwardGateway()
	g.result = []domain.Message{{ID: 32}}
	h := newTestHandler(t, g)

	rec := callMethod(t, h, "copyMessage", `{"chat_id":42,"from_chat_id":43,"message_id":6,"reply_to_message_id":3}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("copyMessage = %d %s", rec.Code, rec.Body.String())
	}
	if call := g.copyCalls[0]; call.setCaption || call.markup != nil || call.replyTo != 3 {
		t.Fatalf("call = %+v", call)
	}
}

func TestCopyMessageRejectsBadInput(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"no message id":   {`{"chat_id":42,"from_chat_id":43}`, "MESSAGE_ID_INVALID"},
		"bad chat":        {`{"chat_id":"x","from_chat_id":43,"message_id":1}`, "CHAT_ID_INVALID"},
		"bad from chat":   {`{"chat_id":42,"message_id":1}`, "CHAT_ID_INVALID"},
		"two reply forms": {`{"chat_id":42,"from_chat_id":43,"message_id":1,"reply_to_message_id":2,"reply_parameters":{"message_id":2}}`, "REPLY_PARAMETERS_INVALID"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeForwardGateway()
			rec := callMethod(t, newTestHandler(t, g), "copyMessage", tc.body)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusBadRequest || resp.Description != tc.want || len(g.copyCalls) != 0 {
				t.Fatalf("copyMessage = %d %s, want %s", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestForwardMethodsNeedForwardGateway(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})
	for _, method := range []string{"forwardMessage", "forwardMessages", "copyMessage", "copyMessages"} {
		rec := callMethod(t, h, method, `{"chat_id":42,"from_chat_id":43,"message_id":1,"message_ids":[1]}`)
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusNotImplemented || resp.Description != "METHOD_NOT_FOUND" {
			t.Fatalf("%s = %d %s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestForwardMessageSurfacesGatewayErrorCode(t *testing.T) {
	g := newFakeForwardGateway()
	g.err = errors.New("rpc error code 400: CHAT_FORWARDS_RESTRICTED")
	rec := callMethod(t, newTestHandler(t, g), "forwardMessage", `{"chat_id":42,"from_chat_id":43,"message_id":1}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "CHAT_FORWARDS_RESTRICTED" {
		t.Fatalf("forwardMessage = %d %s", rec.Code, rec.Body.String())
	}
}

func TestForwardMessagesRejectsBadMessageIDLists(t *testing.T) {
	ids := make([]int, 101)
	for i := range ids {
		ids[i] = i + 1
	}
	tooMany, _ := json.Marshal(ids)
	for name, body := range map[string]string{
		"missing":  `{"chat_id":42,"from_chat_id":43}`,
		"empty":    `{"chat_id":42,"from_chat_id":43,"message_ids":[]}`,
		"not list": `{"chat_id":42,"from_chat_id":43,"message_ids":"7"}`,
		"zero id":  `{"chat_id":42,"from_chat_id":43,"message_ids":[0]}`,
		"too many": `{"chat_id":42,"from_chat_id":43,"message_ids":` + string(tooMany) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeForwardGateway()
			rec := callMethod(t, newTestHandler(t, g), "forwardMessages", body)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusBadRequest || resp.OK || len(g.forwardCalls) != 0 {
				t.Fatalf("forwardMessages = %d %s calls=%d", rec.Code, rec.Body.String(), len(g.forwardCalls))
			}
		})
	}
}
