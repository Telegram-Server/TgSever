package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// forum 话题里的消息带上话题 id（话题的通知设置据此生效）；话题外的消息、普通回复不带。
func TestChannelMessagePushCarriesForumTopic(t *testing.T) {
	forum := domain.Channel{ID: 80, Title: "Forum", Megagroup: true, Forum: true}
	inTopic := domain.ChannelMessage{ChannelID: 80, ID: 9, SenderUserID: 10, From: domain.Peer{Type: domain.PeerTypeUser, ID: 10}, Body: "hi",
		ReplyTo: &domain.MessageReply{MessageID: 4, TopMessageID: 3, ForumTopic: true}}
	got, ok := channelMessagePush(10, domain.SendChannelMessageResult{Channel: forum, Message: inTopic})
	if !ok || got.Chat.TopicID != 3 || got.Chat.ID != 80 {
		t.Fatalf("topic message = %+v ok=%v, want chat 80 topic 3", got.Chat, ok)
	}
	// 直接回复话题根消息：TopMessageID 为 0 时话题就是被回复的那条。
	rootReply := inTopic
	rootReply.ReplyTo = &domain.MessageReply{MessageID: 3, ForumTopic: true}
	if got, _ := channelMessagePush(10, domain.SendChannelMessageResult{Channel: forum, Message: rootReply}); got.Chat.TopicID != 3 {
		t.Fatalf("reply to topic root = %+v, want topic 3", got.Chat)
	}
	plainReply := inTopic
	plainReply.ReplyTo = &domain.MessageReply{MessageID: 4}
	if got, _ := channelMessagePush(10, domain.SendChannelMessageResult{Channel: forum, Message: plainReply}); got.Chat.TopicID != 0 {
		t.Fatalf("reply outside a topic = %+v, want no topic", got.Chat)
	}
}
