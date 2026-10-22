package botapi

import (
	"telesrv/internal/domain"
	"testing"
)

func TestForumServiceProjectionPreservesIconRemovalAndThread(t *testing.T) {
	message := domain.Message{ID: 19, Action: &domain.ChannelMessageAction{Type: domain.ChannelActionTopicEdit, IconEmojiIDSet: true}, ReplyTo: &domain.MessageReply{TopMessageID: 7, ForumTopic: true}}
	out := map[string]any{}
	projectForumMessage(message, out)
	edited, ok := out["forum_topic_edited"].(map[string]any)
	if !ok || edited["icon_custom_emoji_id"] != "" || out["message_thread_id"] != 7 || out["is_topic_message"] != true {
		t.Fatalf("icon removal/thread lost: %v", out)
	}
	if _, ok := edited["name"]; ok {
		t.Fatalf("absent name became an edit: %v", edited)
	}
}

func TestPrivateForumServiceProjectionUsesPersistedAction(t *testing.T) {
	message := domain.Message{ID: 21, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionTopicCreate, ForumTopic: &domain.ChannelMessageAction{Type: domain.ChannelActionTopicCreate, Title: "Private topic", IconColor: 7322096, IconEmojiID: 61}}}}
	out := map[string]any{}
	projectForumMessage(message, out)
	created, ok := out["forum_topic_created"].(map[string]any)
	if !ok || created["name"] != "Private topic" || created["icon_color"] != 7322096 || created["icon_custom_emoji_id"] != "61" || out["message_thread_id"] != 21 || out["is_topic_message"] != true {
		t.Fatalf("private topic service: %v", out)
	}
}
