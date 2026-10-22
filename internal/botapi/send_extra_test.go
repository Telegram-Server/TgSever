package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"telesrv/internal/domain"
)

// fakeSendExtraGateway 记录 SendExtraGateway 各方法收到的参数。
type fakeSendExtraGateway struct {
	*fakeBotAPIGateway

	calls    []string
	chatID   int64
	emoji    string
	action   string
	threadID int

	poll                domain.MessagePoll
	correct             []int
	explanation         string
	explanationEntities []domain.MessageEntity

	items []InputMediaItem
	album []domain.Message

	silent  bool
	protect bool
	replyTo int
	markup  *domain.MessageReplyMarkup
	result  domain.Message
	err     error
}

func newFakeSendExtraGateway() *fakeSendExtraGateway {
	return &fakeSendExtraGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (g *fakeSendExtraGateway) BotAPISendDiceWithOptions(_ context.Context, botID, chatID int64, emoji string, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, _ domain.BotAPIMessageOptions) (domain.Message, error) {
	g.calls = append(g.calls, "dice")
	g.chatID, g.emoji, g.markup, g.silent, g.protect, g.replyTo = chatID, emoji, replyMarkup, silent, protectContent, replyToMessageID
	return g.result, g.err
}

func (g *fakeSendExtraGateway) BotAPISendChatAction(_ context.Context, botID, chatID int64, action string, messageThreadID int) (bool, error) {
	g.calls = append(g.calls, "action")
	g.chatID, g.action, g.threadID = chatID, action, messageThreadID
	return true, g.err
}

func (g *fakeSendExtraGateway) BotAPISendPollWithOptions(_ context.Context, botID, chatID int64, poll domain.MessagePoll, correctOptionIDs []int, explanation string, explanationEntities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, _ domain.BotAPIMessageOptions) (domain.Message, error) {
	g.calls = append(g.calls, "poll")
	g.chatID, g.poll, g.correct, g.explanation, g.explanationEntities = chatID, poll, correctOptionIDs, explanation, explanationEntities
	g.markup, g.silent, g.protect, g.replyTo = replyMarkup, silent, protectContent, replyToMessageID
	return g.result, g.err
}

func (g *fakeSendExtraGateway) BotAPISendMediaGroupWithOptions(_ context.Context, botID, chatID int64, items []InputMediaItem, silent, protectContent bool, replyToMessageID int, _ domain.BotAPIMessageOptions) ([]domain.Message, error) {
	g.calls = append(g.calls, "album")
	g.chatID, g.items, g.silent, g.protect, g.replyTo = chatID, items, silent, protectContent, replyToMessageID
	return g.album, g.err
}

// 骰子的消息投影由 dice_update_test 覆盖；这里只看参数与缺省 emoji 到达网关。
func TestSendDiceDefaultsToDie(t *testing.T) {
	gateway := newFakeSendExtraGateway()
	gateway.result = domain.Message{
		ID: 7, Date: 100, Out: true, OwnerUserID: 1001,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 42},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
	}
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "sendDice", `{"chat_id":42,"disable_notification":true,"reply_to_message_id":3}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.chatID != 42 || gateway.emoji != "🎲" || !gateway.silent || gateway.replyTo != 3 {
		t.Fatalf("gateway call = %+v", gateway)
	}
}

func TestSendDiceReportsTLErrorCode(t *testing.T) {
	gateway := newFakeSendExtraGateway()
	gateway.err = errors.New("rpc error code 400: EMOTICON_INVALID")
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "sendDice", `{"chat_id":42,"emoji":"🍕"}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "EMOTICON_INVALID" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.emoji != "🍕" {
		t.Fatalf("emoji = %q", gateway.emoji)
	}
}

func TestSendChatActionForwardsKnownActions(t *testing.T) {
	gateway := newFakeSendExtraGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "sendChatAction", `{"chat_id":-1001234,"action":"upload_photo","message_thread_id":9}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || string(resp.Result) != "true" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.chatID != -1001234 || gateway.action != "upload_photo" || gateway.threadID != 9 {
		t.Fatalf("gateway call = chat %d action %q thread %d", gateway.chatID, gateway.action, gateway.threadID)
	}

	rec = callMethod(t, h, "sendChatAction", `{"chat_id":42,"action":"dancing"}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "ACTION_INVALID" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(gateway.calls) != 1 {
		t.Fatalf("calls = %v, an unknown action must not reach the gateway", gateway.calls)
	}
}

func TestSendPollParsesQuizAndMixedOptions(t *testing.T) {
	gateway := newFakeSendExtraGateway()
	gateway.result = domain.Message{
		ID: 9, Date: 100, Out: true, OwnerUserID: 1001,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 42},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: &domain.MessagePoll{
			ID: 77, Question: "2+2?", Quiz: true,
			Answers: []domain.MessagePollAnswer{{Text: "3", Option: []byte{0}}, {Text: "4", Option: []byte{1}}},
		}},
	}
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "sendPoll", `{"chat_id":42,"question":"2+2?","options":["3",{"text":"<b>4</b>","text_parse_mode":"HTML"}],`+
		`"type":"quiz","correct_option_id":1,"is_anonymous":false,"explanation":"basic *math*","explanation_parse_mode":"MarkdownV2","open_period":60,"protect_content":true}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	poll := gateway.poll
	if poll.Question != "2+2?" || !poll.Quiz || !poll.PublicVoters || poll.MultipleChoice || poll.ClosePeriod != 60 ||
		len(poll.Answers) != 2 || poll.Answers[0].Text != "3" || poll.Answers[1].Text != "4" ||
		len(poll.Answers[1].Entities) != 1 || poll.Answers[1].Entities[0].Type != domain.MessageEntityBold {
		t.Fatalf("poll = %+v", poll)
	}
	if len(gateway.correct) != 1 || gateway.correct[0] != 1 || gateway.explanation != "basic math" ||
		len(gateway.explanationEntities) != 1 || !gateway.protect {
		t.Fatalf("correct = %v explanation = %q %+v protect %v", gateway.correct, gateway.explanation, gateway.explanationEntities, gateway.protect)
	}
	var msg struct {
		Poll struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Options []struct {
				Text string `json:"text"`
			} `json:"options"`
		} `json:"poll"`
	}
	if err := json.Unmarshal(resp.Result, &msg); err != nil || msg.Poll.ID != "77" || msg.Poll.Type != "quiz" || len(msg.Poll.Options) != 2 {
		t.Fatalf("message = %s (%v)", resp.Result, err)
	}
}

func TestSendPollDefaultsAndValidation(t *testing.T) {
	gateway := newFakeSendExtraGateway()
	gateway.result = domain.Message{ID: 1, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 42}}
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "sendPoll", `{"chat_id":42,"question":"Lunch?","options":["pizza","sushi"],"allows_multiple_answers":true,"is_closed":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.poll.PublicVoters || gateway.poll.Quiz || !gateway.poll.MultipleChoice || !gateway.poll.Closed || len(gateway.correct) != 0 {
		t.Fatalf("poll = %+v correct = %v; want an anonymous closed regular poll", gateway.poll, gateway.correct)
	}

	for _, tc := range []struct{ body, want string }{
		{`{"chat_id":42,"question":"","options":["a","b"]}`, "POLL_QUESTION_INVALID"},
		{`{"chat_id":42,"question":"q","options":"nope"}`, "POLL_ANSWERS_INVALID"},
		{`{"chat_id":42,"question":"q","options":["a","b"],"type":"survey"}`, "POLL_TYPE_INVALID"},
		{`{"chat_id":42,"question":"q","options":["a","b"],"type":"quiz"}`, "QUIZ_CORRECT_ANSWERS_INVALID"},
		{`{"chat_id":42,"question":"q","options":["a","b"],"correct_option_id":0}`, "QUIZ_CORRECT_ANSWERS_INVALID"},
		{`{"chat_id":42,"question":"q","options":["a","b"],"open_period":60,"close_date":1900000000}`, "POLL_CLOSE_INVALID"},
	} {
		rec := callMethod(t, h, "sendPoll", tc.body)
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != tc.want {
			t.Fatalf("%s: status = %d body = %s, want %s", tc.body, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

func TestSendMediaGroupParsesAttachAndURLItems(t *testing.T) {
	gateway := newFakeSendExtraGateway()
	album := []domain.Message{
		{ID: 11, GroupedID: 555, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 42}, Body: "first",
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 1, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 10, H: 10, Size: 3}}}}},
		{ID: 12, GroupedID: 555, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 42},
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 2, MimeType: "video/mp4",
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 1, H: 1, Duration: 3}}}}},
	}
	gateway.album = album
	h := newTestHandler(t, gateway)

	media := `[{"type":"photo","media":"attach://p1","caption":"<b>first</b>","parse_mode":"HTML","has_spoiler":true},` +
		`{"type":"video","media":"https://example.com/v.mp4","width":640,"height":360,"duration":3}]`
	rec := performMultipartBotAPIRequest(t, h, "sendMediaGroup", map[string]string{
		"chat_id": "42", "media": media, "disable_notification": "true",
	}, map[string][]byte{"p1": []byte("png")})
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(gateway.items) != 2 || !gateway.silent || gateway.chatID != 42 {
		t.Fatalf("items = %+v silent = %v", gateway.items, gateway.silent)
	}
	photo, video := gateway.items[0], gateway.items[1]
	if photo.Kind != "photo" || string(photo.File.Bytes) != "png" || photo.Caption != "first" || len(photo.Entities) != 1 || !photo.Spoiler {
		t.Fatalf("photo item = %+v", photo)
	}
	if video.Kind != "video" || video.File.RemoteURL != "https://example.com/v.mp4" || video.File.Width != 640 || video.File.Duration != 3 {
		t.Fatalf("video item = %+v", video)
	}
	var msgs []struct {
		MessageID    int    `json:"message_id"`
		MediaGroupID string `json:"media_group_id"`
		Caption      string `json:"caption"`
	}
	if err := json.Unmarshal(resp.Result, &msgs); err != nil || len(msgs) != 2 {
		t.Fatalf("result = %s (%v)", resp.Result, err)
	}
	if msgs[0].MessageID != 11 || msgs[0].MediaGroupID != "555" || msgs[1].MediaGroupID != "555" || msgs[0].Caption != "first" {
		t.Fatalf("result = %s", resp.Result)
	}
}

func performMultipartBotAPIRequest(t *testing.T, h http.Handler, method string, fields map[string]string, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	for name, data := range files {
		part, err := mw.CreateFormFile(name, name+".bin")
		if err != nil {
			t.Fatalf("create file: %v", err)
		}
		_, _ = part.Write(data)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	token := domain.FormatBotToken(testBotProfile.BotUserID, testBotProfile.TokenSecret)
	req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSendExtraMethodsNeedTheGatewaySurface(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})

	rec := callMethod(t, h, "sendDice", `{"chat_id":42}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusNotImplemented || resp.Description != "METHOD_NOT_FOUND" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}
