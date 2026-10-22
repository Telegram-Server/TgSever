package rpc

import (
	"errors"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	appcontacts "telesrv/internal/app/contacts"
	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"testing"
)

func TestPrivateForumNativePerOwnerThreadMapping(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	if _, err := f.router.BotAPISendMessage(f.ctx, f.bot.ID, f.member.ID, "other dialog", nil, nil, false, false, 0); err != nil {
		t.Fatal(err)
	}
	botCtx := WithUserID(f.ctx, f.bot.ID)
	userPeer := &tg.InputPeerUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash}
	botPeer := &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	updates, err := f.router.onMessagesCreateForumTopic(botCtx, &tg.MessagesCreateForumTopicRequest{Peer: userPeer, Title: "Private native", IconColor: domain.DefaultForumTopicIconColor, RandomID: 98001})
	if err != nil {
		t.Fatal(err)
	}
	var root *tg.MessageService
	for _, update := range updates.(*tg.Updates).Updates {
		if incoming, ok := update.(*tg.UpdateNewMessage); ok {
			root, _ = incoming.Message.(*tg.MessageService)
		}
	}
	if root == nil {
		t.Fatal("native topic create did not expose service message")
	}
	action, ok := root.Action.(*tg.MessageActionTopicCreate)
	if !ok || action.Title != "Private native" {
		t.Fatalf("native create action = %#v", root.Action)
	}
	topic, err := f.messages.GetPrivateForumTopic(f.ctx, f.bot.ID, f.owner.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if topic.ID == topic.PeerTopicID {
		t.Fatal("fixture must have distinct root box IDs")
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: topic.PeerTopicID}
	if _, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendMessageRequest{Peer: botPeer, Message: "native reply", RandomID: 98002, ReplyTo: reply}); err != nil {
		t.Fatal(err)
	}
	native, err := f.router.onMessagesGetReplies(botCtx, &tg.MessagesGetRepliesRequest{Peer: userPeer, MsgID: topic.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := native.(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("native replies = %T", native)
	}
	found := false
	for _, message := range result.Messages {
		if message, ok := message.(*tg.Message); ok && message.Message == "native reply" {
			found = true
			header, ok := message.ReplyTo.(*tg.MessageReplyHeader)
			if !ok || header.ReplyToTopID != topic.ID || !header.ForumTopic {
				t.Fatalf("bot thread header = %#v", message.ReplyTo)
			}
		}
	}
	if !found {
		t.Fatal("native reply missing from bot topic")
	}
	forbidden := &tg.MessagesEditForumTopicRequest{Peer: userPeer, TopicID: topic.ID}
	forbidden.SetClosed(true)
	if _, err := f.router.onMessagesEditForumTopic(botCtx, forbidden); err == nil {
		t.Fatal("private close protocol unexpectedly accepted")
	}
	if _, err := f.router.onMessagesDeleteTopicHistory(botCtx, &tg.MessagesDeleteTopicHistoryRequest{Peer: userPeer, TopMsgID: topic.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.onMessagesGetReplies(WithUserID(f.ctx, f.owner.ID), &tg.MessagesGetRepliesRequest{Peer: botPeer, MsgID: topic.PeerTopicID, Limit: 100}); err == nil {
		t.Fatal("deleted recipient topic still readable")
	}
}

func TestPrivateForumBlockPreventsServiceMessageDelivery(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	contacts := memory.NewContactStore()
	rpcTestBlocklistStores(contacts, f.userStore, memory.NewStoryStore(), memory.NewUpdateEventStore())
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	f.messages = appmessages.NewService(messages, dialogs, appmessages.WithContactStore(contacts))
	f.router.deps.Messages = f.messages
	f.router.deps.Contacts = appcontacts.NewService(contacts, f.userStore)
	topic, err := f.router.botAPICreatePrivateForumTopic(f.ctx, f.bot.ID, f.owner.ID, "Before block", domain.DefaultForumTopicIconColor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = contacts.MutateBlocklist(f.ctx, store.BlocklistMutation{Kind: store.BlocklistBlock, OwnerUserID: f.owner.ID, PeerIDs: []int64{f.bot.ID}, Date: 1700000000}, store.BlocklistDeliveryEffects); err != nil {
		t.Fatal(err)
	}
	name := "Blocked title"
	if _, err = f.router.botAPICreatePrivateForumTopic(f.ctx, f.bot.ID, f.owner.ID, name, domain.DefaultForumTopicIconColor, 0); !tgerr.Is(err, "USER_IS_BLOCKED") {
		t.Fatalf("HTTP bridge blocked create = %v", err)
	}
	if err = f.router.botAPIEditPrivateForumTopic(f.ctx, f.bot.ID, f.owner.ID, topic.TopicID, &name, nil); !tgerr.Is(err, "USER_IS_BLOCKED") {
		t.Fatalf("HTTP bridge blocked edit = %v", err)
	}
	botCtx := WithUserID(f.ctx, f.bot.ID)
	peer := &tg.InputPeerUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash}
	if _, err = f.router.onMessagesCreateForumTopic(botCtx, &tg.MessagesCreateForumTopicRequest{Peer: peer, Title: name, RandomID: 98101}); !tgerr.Is(err, "USER_IS_BLOCKED") {
		t.Fatalf("native blocked create = %v", err)
	}
	edit := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: topic.TopicID}
	edit.SetTitle(name)
	if _, err = f.router.onMessagesEditForumTopic(botCtx, edit); !tgerr.Is(err, "USER_IS_BLOCKED") {
		t.Fatalf("native blocked edit = %v", err)
	}
	if _, err = f.messages.CreatePrivateForumTopic(f.ctx, f.bot.ID, domain.CreatePrivateForumTopicRequest{OwnerUserID: f.bot.ID, PeerUserID: f.owner.ID, Name: name, RandomID: 98102}); !errors.Is(err, domain.ErrPrivateForumBlocked) {
		t.Fatalf("app blocked create = %v", err)
	}
	if _, err = f.messages.EditPrivateForumTopic(f.ctx, f.bot.ID, domain.EditPrivateForumTopicRequest{OwnerUserID: f.bot.ID, PeerUserID: f.owner.ID, TopicID: topic.TopicID, Name: &name, RandomID: 98103}); !errors.Is(err, domain.ErrPrivateForumBlocked) {
		t.Fatalf("app blocked edit = %v", err)
	}
	persisted, err := f.messages.GetPrivateForumTopic(f.ctx, f.bot.ID, f.owner.ID, topic.TopicID)
	if err != nil || persisted.Name != "Before block" {
		t.Fatalf("blocked edit mutated topic = %+v %v", persisted, err)
	}
	inbox, err := messages.ListByUser(f.ctx, f.owner.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 100})
	if err != nil || len(inbox.Messages) != 1 {
		t.Fatalf("blocked operations delivered service messages: %+v %v", inbox.Messages, err)
	}
	action := inbox.Messages[0].Media.ServiceAction
	if action.Kind != domain.MessageServiceActionTopicCreate || action.ForumTopic.Title != "Before block" {
		t.Fatalf("inbox service action = %+v", action)
	}
}
