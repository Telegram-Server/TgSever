package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

// fakeEditExtraGateway 记录 EditExtraGateway 各方法收到的参数。
type fakeEditExtraGateway struct {
	*fakeBotAPIGateway

	calls     []string
	chatID    int64
	messageID int
	inlineID  domain.BotInlineMessageID
	caption   string
	entities  []domain.MessageEntity
	setMarkup bool
	markup    *domain.MessageReplyMarkup
	item      InputMediaItem
	result    domain.Message
	poll      domain.MessagePoll
	err       error
}

func newFakeEditExtraGateway() *fakeEditExtraGateway {
	return &fakeEditExtraGateway{
		fakeBotAPIGateway: &fakeBotAPIGateway{},
		result:            domain.Message{ID: 5, Date: 100, EditDate: 200, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 42}},
	}
}

func (g *fakeEditExtraGateway) BotAPIEditMessageCaption(_ context.Context, botID, chatID int64, messageID int, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	g.calls = append(g.calls, "caption")
	g.chatID, g.messageID, g.caption, g.entities, g.setMarkup, g.markup = chatID, messageID, caption, entities, setReplyMarkup, replyMarkup
	return g.result, g.err
}

func (g *fakeEditExtraGateway) BotAPIEditInlineMessageCaption(_ context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	g.calls = append(g.calls, "inline caption")
	g.inlineID, g.caption, g.entities, g.setMarkup, g.markup = inlineMessageID, caption, entities, setReplyMarkup, replyMarkup
	return true, g.err
}

func (g *fakeEditExtraGateway) BotAPIEditMessageReplyMarkup(_ context.Context, botID, chatID int64, messageID int, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	g.calls = append(g.calls, "markup")
	g.chatID, g.messageID, g.markup = chatID, messageID, replyMarkup
	return g.result, g.err
}

func (g *fakeEditExtraGateway) BotAPIEditInlineMessageReplyMarkup(_ context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	g.calls = append(g.calls, "inline markup")
	g.inlineID, g.markup = inlineMessageID, replyMarkup
	return true, g.err
}

func (g *fakeEditExtraGateway) BotAPIEditMessageMedia(_ context.Context, botID, chatID int64, messageID int, item InputMediaItem, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	g.calls = append(g.calls, "media")
	g.chatID, g.messageID, g.item, g.setMarkup, g.markup = chatID, messageID, item, setReplyMarkup, replyMarkup
	return g.result, g.err
}

func (g *fakeEditExtraGateway) BotAPIEditInlineMessageMedia(_ context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, item InputMediaItem, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	g.calls = append(g.calls, "inline media")
	g.inlineID, g.item, g.setMarkup, g.markup = inlineMessageID, item, setReplyMarkup, replyMarkup
	return true, g.err
}

func (g *fakeEditExtraGateway) BotAPIStopPoll(_ context.Context, botID, chatID int64, messageID int, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.MessagePoll, error) {
	g.calls = append(g.calls, "stop")
	g.chatID, g.messageID, g.setMarkup, g.markup = chatID, messageID, setReplyMarkup, replyMarkup
	return g.poll, g.err
}

func TestEditMessageReplyMarkupSetsAndClears(t *testing.T) {
	gateway := newFakeEditExtraGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "editMessageReplyMarkup", `{"chat_id":42,"message_id":5,"reply_markup":{"inline_keyboard":[[{"text":"Go","callback_data":"go"}]]}}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.messageID != 5 || gateway.markup == nil || len(gateway.markup.Inline) != 1 {
		t.Fatalf("markup = %+v", gateway.markup)
	}

	// 不带 reply_markup 即清空键盘。
	rec = callMethod(t, h, "editMessageReplyMarkup", `{"chat_id":42,"message_id":5}`)
	if rec.Code != http.StatusOK || gateway.markup != nil {
		t.Fatalf("status = %d markup = %+v, want the keyboard cleared", rec.Code, gateway.markup)
	}

	inlineID, _ := encodeBotAPIInlineMessageID(domain.BotInlineMessageID{DCID: 2, OwnerID: 42, ID: 8, AccessHash: 9})
	rec = callMethod(t, h, "editMessageReplyMarkup", `{"inline_message_id":"`+inlineID+`"}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || string(resp.Result) != "true" || gateway.inlineID.ID != 8 {
		t.Fatalf("inline: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestEditMessageMediaParsesInputMedia(t *testing.T) {
	gateway := newFakeEditExtraGateway()
	h := newTestHandler(t, gateway)

	rec := performMultipartBotAPIRequest(t, h, "editMessageMedia", map[string]string{
		"chat_id": "42", "message_id": "5",
		"media": `{"type":"document","media":"attach://f","caption":"*doc*","parse_mode":"MarkdownV2"}`,
	}, map[string][]byte{"f": []byte("data")})
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.item.Kind != "document" || string(gateway.item.File.Bytes) != "data" || gateway.item.Caption != "doc" || len(gateway.item.Entities) != 1 {
		t.Fatalf("item = %+v", gateway.item)
	}

	for _, tc := range []struct{ media, want string }{
		{`{"type":"sticker","media":"https://example.com/a.webp"}`, "MEDIA_INVALID"},
		{`{"type":"photo","media":"attach://none"}`, "FILE_ID_INVALID"},
		{``, "MEDIA_INVALID"},
	} {
		body, _ := json.Marshal(map[string]any{"chat_id": 42, "message_id": 5, "media": tc.media})
		if tc.media != "" {
			body, _ = json.Marshal(map[string]any{"chat_id": 42, "message_id": 5, "media": json.RawMessage(tc.media)})
		}
		rec := callMethod(t, h, "editMessageMedia", string(body))
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != tc.want {
			t.Fatalf("%s: status = %d body = %s, want %s", tc.media, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

func TestStopPollReturnsPoll(t *testing.T) {
	gateway := newFakeEditExtraGateway()
	gateway.poll = domain.MessagePoll{ID: 77, Question: "Lunch?", Closed: true,
		Answers: []domain.MessagePollAnswer{{Text: "pizza", Option: []byte{0}}, {Text: "sushi", Option: []byte{1}}}}
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "stopPoll", `{"chat_id":42,"message_id":5}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var poll struct {
		ID       string `json:"id"`
		IsClosed bool   `json:"is_closed"`
		Options  []any  `json:"options"`
	}
	if err := json.Unmarshal(resp.Result, &poll); err != nil || poll.ID != "77" || !poll.IsClosed || len(poll.Options) != 2 {
		t.Fatalf("result = %s", resp.Result)
	}
	if gateway.chatID != 42 || gateway.messageID != 5 || gateway.setMarkup {
		t.Fatalf("gateway call = %+v", gateway)
	}

	rec = callMethod(t, h, "stopPoll", `{"chat_id":42}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "MESSAGE_ID_INVALID" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestEditMessageCaptionByChatAndMessageID(t *testing.T) {
	gateway := newFakeEditExtraGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "editMessageCaption", `{"chat_id":42,"message_id":5,"caption":"<i>new</i>","parse_mode":"HTML"}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.chatID != 42 || gateway.messageID != 5 || gateway.caption != "new" || len(gateway.entities) != 1 || gateway.setMarkup {
		t.Fatalf("gateway call = %+v", gateway)
	}
	var msg struct {
		MessageID int `json:"message_id"`
		EditDate  int `json:"edit_date"`
	}
	if err := json.Unmarshal(resp.Result, &msg); err != nil || msg.MessageID != 5 || msg.EditDate != 200 {
		t.Fatalf("result = %s", resp.Result)
	}
}

func TestEditMessageCaptionByInlineMessageID(t *testing.T) {
	gateway := newFakeEditExtraGateway()
	h := newTestHandler(t, gateway)
	inlineID, ok := encodeBotAPIInlineMessageID(domain.BotInlineMessageID{DCID: 2, OwnerID: 42, ID: 7, AccessHash: 9})
	if !ok {
		t.Fatal("encode inline id")
	}

	rec := callMethod(t, h, "editMessageCaption", `{"inline_message_id":"`+inlineID+`","caption":"","reply_markup":{"inline_keyboard":[[{"text":"Go","callback_data":"go"}]]}}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || string(resp.Result) != "true" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.inlineID.ID != 7 || gateway.caption != "" || !gateway.setMarkup || gateway.markup == nil {
		t.Fatalf("gateway call = %+v", gateway)
	}
}

func TestEditMethodsValidateMessageIdentifier(t *testing.T) {
	gateway := newFakeEditExtraGateway()
	h := newTestHandler(t, gateway)

	for _, tc := range []struct{ body, want string }{
		{`{"message_id":5,"caption":"x"}`, "CHAT_ID_INVALID"},
		{`{"chat_id":42,"caption":"x"}`, "MESSAGE_ID_INVALID"},
		{`{"chat_id":42,"message_id":5,"inline_message_id":"abc","caption":"x"}`, "MESSAGE_IDENTIFIER_INVALID"},
	} {
		rec := callMethod(t, h, "editMessageCaption", tc.body)
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != tc.want {
			t.Fatalf("%s: status = %d body = %s, want %s", tc.body, rec.Code, rec.Body.String(), tc.want)
		}
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("calls = %v", gateway.calls)
	}
}
