package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func forumHTTP(t *testing.T, f botAPIBridgeFixture) func(string, map[string]any, bool) any {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = server.Close() })
	return func(method string, params map[string]any, wantOK bool) any {
		t.Helper()
		body, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, req)
		var decoded struct {
			OK     bool `json:"ok"`
			Result any  `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || decoded.OK != wantOK {
			t.Fatalf("%s: %s (%v), want ok=%v", method, response.Body.String(), err, wantOK)
		}
		return decoded.Result
	}
}

func TestBotAPIForumHTTPLifecycle(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	if _, err := f.channels.SetForum(f.ctx, f.owner.ID, f.channel.ID, true, false); err != nil {
		t.Fatal(err)
	}
	call := forumHTTP(t, f)
	// A free emoji in another system catalog must not become a forum icon.
	for _, id := range []int64{901, 902} {
		f.files.docs[id] = domain.Document{ID: id, MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrImageSize, W: 100, H: 100}, {Kind: domain.DocAttrCustomEmoji, Alt: "⭐", Free: true}}}
	}
	f.files.sets = map[domain.StickerSetKind][]domain.StickerSet{domain.StickerSetKindSystem: {{ID: 81, SystemKey: domain.StickerSetSystemKeyEmojiDefaultTopicIcons, DocumentIDs: []int64{901}}, {ID: 82, SystemKey: domain.StickerSetSystemKeyEmojiDefaultStatuses, DocumentIDs: []int64{902}}}}
	icons := call("getForumTopicIconStickers", nil, true).([]any)
	if len(icons) != 1 || icons[0].(map[string]any)["custom_emoji_id"] != "901" || icons[0].(map[string]any)["type"] != "custom_emoji" {
		t.Fatalf("wrong icon catalog: %v", icons)
	}
	call("createForumTopic", map[string]any{"chat_id": f.chatID, "name": "No admin"}, false)
	f.promoteBot(t, domain.ChannelAdminRights{ManageTopics: true, PinMessages: true})
	call("createForumTopic", map[string]any{"chat_id": f.chatID, "name": "Wrong icon", "icon_custom_emoji_id": "902"}, false)
	call("createForumTopic", map[string]any{"chat_id": f.chatID, "name": "Wrong color", "icon_color": 123}, false)
	topic := call("createForumTopic", map[string]any{"chat_id": f.chatID, "name": "Alpha", "icon_custom_emoji_id": "901", "icon_color": 7322096}, true).(map[string]any)
	id := int(topic["message_thread_id"].(float64))
	if id <= 1 || topic["name"] != "Alpha" || topic["icon_custom_emoji_id"] != "901" {
		t.Fatalf("created topic: %v", topic)
	}
	call("editForumTopic", map[string]any{"chat_id": f.chatID, "message_thread_id": id, "name": "Renamed", "icon_custom_emoji_id": ""}, true)
	call("closeForumTopic", map[string]any{"chat_id": f.chatID, "message_thread_id": id}, true)
	call("reopenForumTopic", map[string]any{"chat_id": f.chatID, "message_thread_id": id}, true)
	call("editGeneralForumTopic", map[string]any{"chat_id": f.chatID, "name": "Lobby"}, true)
	call("closeGeneralForumTopic", map[string]any{"chat_id": f.chatID}, true)
	call("reopenGeneralForumTopic", map[string]any{"chat_id": f.chatID}, true)
	call("hideGeneralForumTopic", map[string]any{"chat_id": f.chatID}, true)
	call("unhideGeneralForumTopic", map[string]any{"chat_id": f.chatID}, true)
	general, err := f.channels.GeneralForumTopic(f.ctx, f.member.ID, f.channel.ID)
	if err != nil || general.Title != "Lobby" || general.Hidden || !general.Closed {
		t.Fatalf("General state: %+v, %v", general, err)
	}
	call("reopenGeneralForumTopic", map[string]any{"chat_id": f.chatID}, true)
	pinned := call("sendMessage", map[string]any{"chat_id": f.chatID, "message_thread_id": id, "text": "Alpha pin"}, true).(map[string]any)
	messageID := int(pinned["message_id"].(float64))
	call("pinChatMessage", map[string]any{"chat_id": f.chatID, "message_id": messageID}, true)
	call("unpinAllForumTopicMessages", map[string]any{"chat_id": f.chatID, "message_thread_id": id}, true)
	call("unpinAllGeneralForumTopicMessages", map[string]any{"chat_id": f.chatID}, true)
	// Creating/managing a topic is not the right to delete its history.
	call("deleteForumTopic", map[string]any{"chat_id": f.chatID, "message_thread_id": id}, false)
	f.promoteBot(t, domain.ChannelAdminRights{ManageTopics: true, PinMessages: true, DeleteMessages: true})
	call("deleteForumTopic", map[string]any{"chat_id": f.chatID, "message_thread_id": 1}, false)
	call("deleteForumTopic", map[string]any{"chat_id": f.chatID, "message_thread_id": id}, true)
	topics, err := f.channels.GetForumTopicsByID(f.ctx, f.member.ID, f.channel.ID, []int{id})
	if err != nil || len(topics.Topics) != 0 {
		t.Fatalf("deleted topic still visible: %+v, %v", topics, err)
	}
	// Bots do not receive echoes of their own outgoing updates. Exercise service
	// updates authored by the owner and consume them through the real HTTP queue.
	ownerTopic, err := f.channels.CreateForumTopic(f.ctx, f.owner.ID, domain.CreateChannelForumTopicRequest{UserID: f.owner.ID, ChannelID: f.channel.ID, Title: "Owner topic", RandomID: 730001, Date: 1700000900})
	if err != nil {
		t.Fatal(err)
	}
	// The RPC adapter performs live fanout; the store result above supplies the
	// persistent topic identity without assuming its allocation.
	peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	titleEdit := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: ownerTopic.Topic.TopicID}
	titleEdit.SetTitle("Owner renamed")
	if _, err := f.router.onMessagesEditForumTopic(WithUserID(f.ctx, f.owner.ID), titleEdit); err != nil {
		t.Fatal(err)
	}
	// Create another root through RPC so forum_topic_created is queued too.
	if _, err := f.router.onMessagesCreateForumTopic(WithUserID(f.ctx, f.owner.ID), &tg.MessagesCreateForumTopicRequest{Peer: peer, Title: "Visible create", RandomID: 730002}); err != nil {
		t.Fatal(err)
	}
	for _, closed := range []bool{true, false} {
		req := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: ownerTopic.Topic.TopicID}
		req.SetClosed(closed)
		if _, err := f.router.onMessagesEditForumTopic(WithUserID(f.ctx, f.owner.ID), req); err != nil {
			t.Fatal(err)
		}
	}
	for _, hidden := range []bool{true, false} {
		req := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: 1}
		req.SetHidden(hidden)
		if _, err := f.router.onMessagesEditForumTopic(WithUserID(f.ctx, f.owner.ID), req); err != nil {
			t.Fatal(err)
		}
	}
	updates := call("getUpdates", nil, true).([]any)
	seen := map[string]bool{}
	for _, raw := range updates {
		update := raw.(map[string]any)
		for _, kind := range []string{"message", "channel_post"} {
			if msg, ok := update[kind].(map[string]any); ok {
				for _, field := range []string{"forum_topic_created", "forum_topic_edited", "forum_topic_closed", "forum_topic_reopened", "general_forum_topic_hidden", "general_forum_topic_unhidden"} {
					if _, ok := msg[field]; ok {
						seen[field] = true
					}
				}
			}
		}
	}
	for _, field := range []string{"forum_topic_created", "forum_topic_edited", "forum_topic_closed", "forum_topic_reopened", "general_forum_topic_hidden", "general_forum_topic_unhidden"} {
		if !seen[field] {
			t.Fatalf("missing public service update %s: %v", field, seen)
		}
	}
}

func TestBotAPIPrivateForumHTTPLifecycle(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := forumHTTP(t, f)
	create := func(name string) int {
		t.Helper()
		topic := call("createForumTopic", map[string]any{"chat_id": f.member.ID, "name": name}, true).(map[string]any)
		return int(topic["message_thread_id"].(float64))
	}
	a, b := create("Private Alpha"), create("Private Beta")
	call("editForumTopic", map[string]any{"chat_id": f.member.ID, "message_thread_id": a, "name": "Renamed private Alpha", "icon_custom_emoji_id": ""}, true)
	call("closeForumTopic", map[string]any{"chat_id": f.member.ID, "message_thread_id": a}, false)
	call("unpinAllGeneralForumTopicMessages", map[string]any{"chat_id": f.member.ID}, false)
	send := func(thread int, text string) int {
		t.Helper()
		message := call("sendMessage", map[string]any{"chat_id": f.member.ID, "message_thread_id": thread, "text": text}, true).(map[string]any)
		if message["message_thread_id"] != float64(thread) || message["is_topic_message"] != true {
			t.Fatalf("lost private thread: %v", message)
		}
		return int(message["message_id"].(float64))
	}
	aMessage, bMessage := send(a, "A"), send(b, "B")
	for _, id := range []int{aMessage, bMessage} {
		call("pinChatMessage", map[string]any{"chat_id": f.member.ID, "message_id": id}, true)
	}
	call("unpinAllForumTopicMessages", map[string]any{"chat_id": f.member.ID, "message_thread_id": a}, true)
	list, err := f.messages.GetMessages(f.ctx, f.bot.ID, []int{aMessage, bMessage})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range list.Messages {
		if message.ID == aMessage && message.Pinned || message.ID == bMessage && !message.Pinned {
			t.Fatalf("private unpin escaped thread: %+v", message)
		}
	}
	call("deleteForumTopic", map[string]any{"chat_id": f.member.ID, "message_thread_id": a}, true)
	call("sendMessage", map[string]any{"chat_id": f.member.ID, "message_thread_id": a, "text": "deleted thread"}, false)
	list, err = f.messages.GetMessages(f.ctx, f.bot.ID, []int{aMessage, bMessage})
	if err != nil {
		t.Fatal(err)
	}
	foundB := false
	for _, message := range list.Messages {
		if message.ID == aMessage {
			t.Fatal("deleted private thread message survived")
		}
		if message.ID == bMessage {
			foundB = message.Pinned
		}
	}
	if !foundB {
		t.Fatal("deleting private Alpha affected Beta")
	}
	send(b, "Beta survives")
}
