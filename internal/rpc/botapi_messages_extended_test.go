package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestBotAPIExtendedMessagesHTTPStateTransitions(t *testing.T) {
	f, _, connection := newBusinessFixture(t, domain.BusinessBotRights{Reply: true})
	f.router.deps.MessageDrafts = memory.NewMessageDraftStore()
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	now := int(f.router.clock.Now().Unix())
	if _, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 87001, Message: "start business", Date: now}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	call := func(method string, fields map[string]any, want bool) map[string]any {
		t.Helper()
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["ok"] != want {
			t.Fatalf("%s=%s", method, response.Body.String())
		}
		return result
	}
	sent, err := f.router.BotAPISendMessage(f.ctx, f.bot.ID, f.chatID, "before", nil, nil, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := f.router.BotAPIEditMessageText(f.ctx, f.bot.ID, f.chatID, sent.ID, "after", nil, false, nil, false)
	if err != nil || edited.Body != "after" {
		t.Fatalf("group edit=%+v %v", edited, err)
	}
	call("setMessageReaction", map[string]any{"chat_id": f.chatID, "message_id": sent.ID, "reaction": []any{map[string]any{"type": "paid"}}}, false)
	call("setMessageReaction", map[string]any{"chat_id": f.chatID, "message_id": sent.ID, "reaction": []any{map[string]any{"type": "emoji", "emoji": "not an emoji"}}}, false)
	call("setMessageReaction", map[string]any{"chat_id": f.chatID, "message_id": sent.ID, "reaction": []any{map[string]any{"type": "emoji", "emoji": "👍"}}}, true)
	actor := domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}
	addActor := func() {
		t.Helper()
		_, err := f.channels.SetMessageReactions(f.ctx, f.owner.ID, domain.SetChannelMessageReactionsRequest{Actor: actor, ChannelID: f.channel.ID, MessageID: sent.ID, Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}}, Date: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	addActor()
	call("deleteMessageReaction", map[string]any{"chat_id": f.chatID, "message_id": sent.ID, "actor_chat_id": f.chatID}, true)
	addActor()
	call("deleteAllMessageReactions", map[string]any{"chat_id": f.chatID, "actor_chat_id": f.chatID}, true)
	state, err := f.channels.GetMessageReactions(f.ctx, f.bot.ID, domain.ChannelMessageReactionsRequest{ChannelID: f.channel.ID, IDs: []int{sent.ID}})
	if err != nil || len(state.Reactions.Results) != 1 || state.Reactions.Results[0].Reaction.Emoticon != "👍" {
		t.Fatalf("moderation damaged bot reaction=%+v %v", state, err)
	}
	call("deleteMessages", map[string]any{"chat_id": f.chatID, "message_ids": []int{sent.ID, 999999}}, true)
	history, err := f.channels.GetMessages(f.ctx, f.bot.ID, f.channel.ID, []int{sent.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range history.Messages {
		if !msg.Deleted {
			t.Fatalf("bulk delete not persisted: %+v", msg)
		}
	}
	live, err := f.router.BotAPISendOrdinary(f.ctx, domain.BotAPIEphemeralSendInput{BotUserID: f.bot.ID, ChatID: f.member.ID, Kind: "location", DirectMedia: &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Period: 900}}}, domain.BotAPIMessageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call("editMessageLiveLocation", map[string]any{"chat_id": f.member.ID, "message_id": live.ID, "latitude": 3, "longitude": 4}, true)
	call("stopMessageLiveLocation", map[string]any{"chat_id": f.member.ID, "message_id": live.ID}, true)
	call("editMessageLiveLocation", map[string]any{"chat_id": f.member.ID, "message_id": live.ID, "latitude": 5, "longitude": 6}, false)
	checklist := map[string]any{"title": "Shopping", "others_can_mark_tasks_as_done": true, "tasks": []any{map[string]any{"id": 7, "text": "Milk"}}}
	result := call("sendChecklist", map[string]any{"business_connection_id": connection.ConnectionID, "chat_id": f.member.ID, "checklist": checklist}, true)
	object := result["result"].(map[string]any)
	id := int(object["message_id"].(float64))
	if _, err := f.router.onMessagesToggleTodoCompleted(WithUserID(f.ctx, f.owner.ID), &tg.MessagesToggleTodoCompletedRequest{Peer: &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}, MsgID: id, Completed: []int{7}}); err != nil {
		t.Fatal(err)
	}
	checklist["title"] = "Updated"
	call("editMessageChecklist", map[string]any{"business_connection_id": connection.ConnectionID, "chat_id": f.member.ID, "message_id": id, "checklist": checklist}, true)
	stored, err := f.router.botAPIReloadMessage(f.ctx, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}, id)
	if err != nil || stored.Media.Todo.Title != "Updated" || len(stored.Media.Todo.Completions) != 1 || stored.Media.Todo.Completions[0].CompletedBy != f.owner.ID {
		t.Fatalf("checklist completion lost=%+v %v", stored, err)
	}
	call("sendMessageDraft", map[string]any{"chat_id": f.member.ID, "draft_id": 71, "text": "partial", "can_stop": true}, true)
	call("sendRichMessageDraft", map[string]any{"chat_id": f.member.ID, "draft_id": 72, "rich_message": map[string]any{"html": "<p>Rich partial</p>"}, "can_stop": true, "keep_on_stop": true}, true)
	call("sendMessageDraft", map[string]any{"chat_id": f.member.ID, "draft_id": 0, "text": "bad"}, false)
}

func TestBotAPIOrdinaryMediaPersistsBothMessageKinds(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	for _, chatID := range []int64{f.member.ID, f.chatID} {
		for _, media := range []*domain.MessageMedia{
			{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{PhoneNumber: "15551234567", FirstName: "Ada"}},
			{Kind: domain.MessageMediaKindGeo, Geo: &domain.MessageGeoPoint{Lat: 10, Long: 20}},
			{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{Geo: domain.MessageGeoPoint{Lat: 10, Long: 20}, Title: "Station", Address: "Road"}},
		} {
			sent, err := f.router.BotAPISendOrdinary(f.ctx, domain.BotAPIEphemeralSendInput{BotUserID: f.bot.ID, ChatID: chatID, Kind: "direct", DirectMedia: media}, domain.BotAPIMessageOptions{})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := f.router.botAPIReloadMessage(f.ctx, f.bot.ID, sent.Peer, sent.ID)
			if err != nil || loaded.Media == nil || loaded.Media.Kind != media.Kind {
				t.Fatalf("ordinary media lost=%+v %v", loaded, err)
			}
		}
	}
}

func (f *fakeFiles) CreateDocumentFromURLWithSpec(ctx context.Context, rawURL string, spec domain.DocumentSpec) (domain.Document, error) {
	doc, err := f.CreateDocumentFromURL(ctx, rawURL)
	if err != nil {
		return doc, err
	}
	doc.Attributes = spec.Attributes
	if spec.MimeType != "" {
		doc.MimeType = spec.MimeType
	}
	if f.docs == nil {
		f.docs = make(map[int64]domain.Document)
	}
	f.docs[doc.ID] = doc
	return doc, nil
}

func TestBotAPIMediaKindsPersistReusableAttributes(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	for _, kind := range []string{"animation", "audio", "video", "voice", "sticker", "video_note"} {
		file := domain.BotAPIFileInput{Bytes: []byte("media bytes"), FileName: "asset", MimeType: "application/octet-stream", Width: 320, Height: 320, Duration: 4, Title: "Title", Performer: "Performer", Emoji: "🙂", SupportsStreaming: true}
		sent, err := f.router.BotAPISendMediaFiles(f.ctx, f.bot.ID, f.member.ID, kind, file, domain.BotAPIFileInput{}, "caption", nil, nil, false, 0)
		if err != nil {
			t.Fatalf("%s=%v", kind, err)
		}
		if sent.Media == nil || sent.Media.Document == nil || !botAPIDocumentMatchesKind(*sent.Media.Document, kind) {
			t.Fatalf("%s attrs=%+v", kind, sent.Media)
		}
		reused, err := f.router.BotAPISendMediaFiles(f.ctx, f.bot.ID, f.chatID, kind, domain.BotAPIFileInput{LocationKey: fmt.Sprintf("doc:%d", sent.Media.Document.ID)}, domain.BotAPIFileInput{}, "reused", nil, nil, false, 0)
		if err != nil || reused.Media == nil || reused.Media.Document == nil || reused.Media.Document.ID != sent.Media.Document.ID {
			t.Fatalf("%s reused=%+v %v", kind, reused, err)
		}
	}
}
