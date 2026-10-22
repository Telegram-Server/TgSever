package storetest

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

// PrivateForum verifies the consumer-visible private topic lifecycle, both box
// projections, chat isolation and revocation through a freshly opened store.
func PrivateForum(t *testing.T, messages store.MessageStore, reopen func() store.MessageStore, channels store.ChannelStore, owner, peer, other int64) {
	t.Helper()
	ctx := context.Background()
	topics := messages.(store.PrivateForumStore)
	privateSource, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: owner, RecipientUserID: other, RandomID: 97000, Message: "unrelated dialog", Date: 1699999999})
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, random int64) domain.PrivateForumTopic {
		t.Helper()
		result, err := topics.CreatePrivateForumTopic(ctx, domain.CreatePrivateForumTopicRequest{OwnerUserID: owner, PeerUserID: peer, Name: name, IconColor: domain.DefaultForumTopicIconColor, RandomID: random, Date: 1700000000})
		if err != nil {
			t.Fatal(err)
		}
		if result.Message.SenderMessage.Media.ServiceAction.ForumTopic.Title != name {
			t.Fatal("missing create service action")
		}
		return result.Topic
	}
	a, b := create("A", 97001), create("B", 97002)
	if _, err := topics.CreatePrivateForumTopic(ctx, domain.CreatePrivateForumTopicRequest{OwnerUserID: owner, PeerUserID: peer, Name: "blocked create", RandomID: 97012, RecipientBlocked: true}); !errors.Is(err, domain.ErrPrivateForumBlocked) {
		t.Fatalf("blocked create = %v", err)
	}
	blockedName := "blocked edit"
	if _, err := topics.EditPrivateForumTopic(ctx, domain.EditPrivateForumTopicRequest{OwnerUserID: owner, PeerUserID: peer, TopicID: a.ID, Name: &blockedName, RandomID: 97013, RecipientBlocked: true}); !errors.Is(err, domain.ErrPrivateForumBlocked) {
		t.Fatalf("blocked edit = %v", err)
	}
	send := func(topic int, random int64, text string) domain.SendPrivateTextResult {
		t.Helper()
		req := domain.SendPrivateTextRequest{SenderUserID: owner, RecipientUserID: peer, RandomID: random, Message: text, Date: 1700000001}
		if topic != 0 {
			req.ReplyTo = &domain.MessageReply{TopMessageID: topic, ForumTopic: true}
		}
		result, err := messages.SendPrivateText(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	am, bm, plain := send(a.ID, 97003, "A message"), send(b.ID, 97004, "B message"), send(0, 97005, "plain message")
	if domain.PrivateForumMessageTopic(am.SenderMessage) != a.ID || domain.PrivateForumMessageTopic(am.RecipientMessage) != a.PeerTopicID {
		t.Fatal("per-owner thread identity lost in send projection")
	}
	if am.SenderMessage.ID == am.RecipientMessage.ID {
		t.Fatal("fixture must use different per-owner message box IDs")
	}
	inherited, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: peer, RecipientUserID: owner, RandomID: 97009, Message: "reply in A", ReplyTo: &domain.MessageReply{MessageID: am.RecipientMessage.ID}, Date: 1700000002})
	if err != nil || domain.PrivateForumMessageTopic(inherited.SenderMessage) != a.PeerTopicID || domain.PrivateForumMessageTopic(inherited.RecipientMessage) != a.ID {
		t.Fatalf("reply lost per-owner thread: %+v %v", inherited, err)
	}
	rootReply, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: peer, RecipientUserID: owner, RandomID: 97014, Message: "reply to A root", ReplyTo: &domain.MessageReply{MessageID: a.PeerTopicID}, Date: 1700000002})
	if err != nil || domain.PrivateForumMessageTopic(rootReply.SenderMessage) != a.PeerTopicID || domain.PrivateForumMessageTopic(rootReply.RecipientMessage) != a.ID {
		t.Fatalf("root reply escaped topic: %+v %v", rootReply, err)
	}
	sourceChannel, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "External reply source", Megagroup: true, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	channelSource, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: owner, ChannelID: sourceChannel.Channel.ID, Message: "channel source", RandomID: 97015, Date: 1700000001})
	if err != nil {
		t.Fatal(err)
	}
	externalIDs := make([]int, 0, 2)
	for i, source := range []struct {
		peer domain.Peer
		id   int
		text string
	}{{domain.Peer{Type: domain.PeerTypeUser, ID: other}, privateSource.SenderMessage.ID, "unrelated dialog"}, {domain.Peer{Type: domain.PeerTypeChannel, ID: sourceChannel.Channel.ID}, channelSource.Message.ID, "channel source"}} {
		result, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: owner, RecipientUserID: peer, RandomID: 97016 + int64(i), Message: "external reply in A", Date: 1700000002, ReplyTo: &domain.MessageReply{Peer: source.peer, MessageID: source.id, TopMessageID: a.ID, ForumTopic: true}})
		if err != nil {
			t.Fatal(err)
		}
		sender, recipient := result.SenderMessage.ReplyTo, result.RecipientMessage.ReplyTo
		if sender == nil || recipient == nil || sender.Peer != source.peer || recipient.Peer != source.peer || sender.MessageID != source.id || sender.External == nil || recipient.External == nil || sender.External.Text != source.text || recipient.External.Text != source.text || domain.PrivateForumMessageTopic(result.SenderMessage) != a.ID || domain.PrivateForumMessageTopic(result.RecipientMessage) != a.PeerTopicID {
			t.Fatalf("external source or destination thread corrupted: %+v", result)
		}
		if source.peer.Type == domain.PeerTypeUser && recipient.MessageID != 0 {
			t.Fatal("private source leaked owner-local jump ID")
		}
		if source.peer.Type == domain.PeerTypeChannel && recipient.MessageID != source.id {
			t.Fatal("channel source jump ID lost")
		}
		externalIDs = append(externalIDs, result.SenderMessage.ID)
	}
	name := "Renamed A"
	if _, err := topics.EditPrivateForumTopic(ctx, domain.EditPrivateForumTopicRequest{OwnerUserID: owner, PeerUserID: peer, TopicID: a.ID, Name: &name, RandomID: 97006, Date: 1700000002}); err != nil {
		t.Fatal(err)
	}
	messages = reopen()
	topics = messages.(store.PrivateForumStore)
	topic, err := topics.GetPrivateForumTopic(ctx, owner, peer, a.ID)
	if err != nil || topic.Name != name {
		t.Fatalf("durable rename = %+v %v", topic, err)
	}
	externalMessages, err := messages.GetByIDs(ctx, owner, externalIDs)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range externalMessages.Messages {
		if domain.PrivateForumMessageTopic(message) != a.ID || message.ReplyTo.External == nil {
			t.Fatalf("persistent external topic header lost: %+v", message)
		}
	}
	list, err := topics.ListPrivateForumTopicMessages(ctx, owner, peer, a.ID, domain.MessageFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen, seenRoot := false, false
	for _, msg := range list.Messages {
		if msg.Body == "A message" {
			seen = true
		}
		if msg.Body == "reply to A root" {
			seenRoot = true
		}
		if msg.Body == "B message" || msg.Body == "plain message" {
			t.Fatal("cross-topic history leak")
		}
	}
	if !seen || !seenRoot {
		t.Fatal("topic message or root-only reply absent")
	}
	for _, msg := range list.Messages {
		if domain.PrivateForumMessageTopic(msg) != a.ID {
			t.Fatalf("persisted thread lost for message %d", msg.ID)
		}
	}
	received, err := topics.ListPrivateForumTopicMessages(ctx, peer, owner, a.PeerTopicID, domain.MessageFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen = false
	for _, msg := range received.Messages {
		if msg.Body == "A message" {
			seen = true
		}
		if domain.PrivateForumMessageTopic(msg) != a.PeerTopicID {
			t.Fatalf("recipient thread lost for message %d", msg.ID)
		}
	}
	if !seen {
		t.Fatal("recipient topic message absent")
	}
	for _, msg := range []domain.Message{am.SenderMessage, bm.SenderMessage, plain.SenderMessage} {
		if _, err := messages.PinPrivateMessage(ctx, domain.PinPrivateMessageRequest{OwnerUserID: owner, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peer}, MessageID: msg.ID, Pinned: true, PmOneside: true, Date: 1700000003}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := messages.UnpinAllPrivateMessages(ctx, domain.UnpinAllPrivateMessagesRequest{OwnerUserID: owner, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peer}, TopicID: a.ID, Date: 1700000004}); err != nil {
		t.Fatal(err)
	}
	pins, err := messages.GetByIDs(ctx, owner, []int{am.SenderMessage.ID, bm.SenderMessage.ID, plain.SenderMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range pins.Messages {
		want := msg.ID != am.SenderMessage.ID
		if msg.Pinned != want {
			t.Fatalf("pin isolation %d: %v want %v", msg.ID, msg.Pinned, want)
		}
	}
	if _, err := topics.GetPrivateForumTopic(ctx, other, peer, a.ID); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
	if _, err := topics.EditPrivateForumTopic(ctx, domain.EditPrivateForumTopicRequest{OwnerUserID: other, PeerUserID: peer, TopicID: a.ID, Name: &name, RandomID: 97010}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("foreign editor accepted: %v", err)
	}
	if _, err := topics.DeletePrivateForumTopic(ctx, owner, other, a.ID, 1700000005); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("foreign chat accepted: %v", err)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: owner, RecipientUserID: other, RandomID: 97007, Message: "leak", ReplyTo: &domain.MessageReply{TopMessageID: a.ID, ForumTopic: true}}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("foreign chat send accepted: %v", err)
	}
	orphan := send(a.ID, 97011, "recipient-only A")
	if _, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{OwnerUserID: owner, IDs: []int{orphan.SenderMessage.ID}, Revoke: false, Date: 1700000005}); err != nil {
		t.Fatal(err)
	}
	if _, err := topics.DeletePrivateForumTopic(ctx, owner, peer, a.ID, 1700000006); err != nil {
		t.Fatal(err)
	}
	deletedRootReply, err := messages.GetByIDs(ctx, peer, []int{rootReply.SenderMessage.ID})
	if err != nil || len(deletedRootReply.Messages) != 0 {
		t.Fatalf("root reply survived deletion: %+v %v", deletedRootReply, err)
	}
	messages = reopen()
	topics = messages.(store.PrivateForumStore)
	if _, err := topics.GetPrivateForumTopic(ctx, owner, peer, a.ID); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("deleted topic survived: %v", err)
	}
	remaining, err := messages.GetByIDs(ctx, owner, []int{am.SenderMessage.ID, bm.SenderMessage.ID, plain.SenderMessage.ID, privateSource.SenderMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]bool{}
	for _, msg := range remaining.Messages {
		bodies[msg.Body] = true
	}
	if bodies["A message"] || !bodies["B message"] || !bodies["plain message"] || !bodies["unrelated dialog"] {
		t.Fatalf("delete isolation = %v", bodies)
	}
	receiver, err := messages.GetByIDs(ctx, peer, []int{am.RecipientMessage.ID, bm.RecipientMessage.ID, plain.RecipientMessage.ID, orphan.RecipientMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	bodies = map[string]bool{}
	for _, msg := range receiver.Messages {
		bodies[msg.Body] = true
	}
	if bodies["A message"] || bodies["recipient-only A"] || !bodies["B message"] || !bodies["plain message"] {
		t.Fatalf("recipient delete isolation = %v", bodies)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: owner, RecipientUserID: peer, RandomID: 97008, Message: "deleted", ReplyTo: &domain.MessageReply{TopMessageID: a.ID, ForumTopic: true}}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("deleted topic send accepted: %v", err)
	}
}
