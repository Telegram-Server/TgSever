package storetest

import (
	"context"
	"telesrv/internal/domain"
	"testing"
)

func BotAPIReplyParameters(t *testing.T, f ReactionChangeFixture) {
	t.Helper()
	ctx := context.Background()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.Member}
	source, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 44001, Message: "😀hello", Entities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 2, Length: 5}}, Date: 1700010000})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 44002, Message: "quoted", ReplyTo: &domain.MessageReply{Peer: peer, MessageID: source.SenderMessage.ID, QuoteText: "hello", QuoteOffset: 2, QuoteEntities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 5}}}, Date: 1700010001}
	quoted, err := f.Messages.SendPrivateText(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.Messages.GetByIDs(ctx, f.Member, []int{quoted.RecipientMessage.ID})
	if err != nil || len(read.Messages) != 1 || read.Messages[0].ReplyTo == nil || read.Messages[0].ReplyTo.QuoteOffset != 2 || len(read.Messages[0].ReplyTo.QuoteEntities) != 1 {
		t.Fatalf("quote roundtrip=%+v %v", read, err)
	}
	request.RandomID = 44003
	request.ReplyTo.QuoteEntities = nil
	if _, err := f.Messages.SendPrivateText(ctx, request); err == nil {
		t.Fatal("source formatting mismatch accepted")
	}
	missing := domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 44004, Message: "fallback", ReplyTo: &domain.MessageReply{Peer: peer, MessageID: quoted.SenderMessage.ID + 1, AllowMissing: true}, Date: 1700010002}
	fallback, err := f.Messages.SendPrivateText(ctx, missing)
	if err != nil || fallback.SenderMessage.ReplyTo != nil {
		t.Fatalf("missing fallback=%+v %v", fallback, err)
	}
	// The fallback itself now occupies the previously missing box ID. Replaying
	// must use the immutable receipt, not turn that fallback into a self-reply.
	replay, err := f.Messages.SendPrivateText(ctx, missing)
	if err != nil || !replay.Duplicate || replay.SenderMessage.ID != fallback.SenderMessage.ID || replay.SenderMessage.ReplyTo != nil {
		t.Fatalf("missing replay=%+v %v", replay, err)
	}
	todo, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 44005, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{Title: "List", Items: []domain.MessageTodoItem{{ID: 7, Title: "milk"}}}}, Date: 1700010003})
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 44006, Message: "task reply", ReplyTo: &domain.MessageReply{Peer: peer, MessageID: todo.SenderMessage.ID, ChecklistTaskID: 7}, Date: 1700010004})
	if err != nil {
		t.Fatal(err)
	}
	read, err = f.Messages.GetByIDs(ctx, f.Member, []int{task.RecipientMessage.ID})
	if err != nil || len(read.Messages) != 1 || read.Messages[0].ReplyTo == nil || read.Messages[0].ReplyTo.ChecklistTaskID != 7 {
		t.Fatalf("task reply lost=%+v %v", read, err)
	}
	if _, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 44007, Message: "bad task", ReplyTo: &domain.MessageReply{Peer: peer, MessageID: todo.SenderMessage.ID, ChecklistTaskID: 99}, Date: 1700010005}); err == nil {
		t.Fatal("nonexistent task accepted")
	}
}

func BotAPIChannelReplyParameters(t *testing.T, f ReactionChangeFixture) {
	t.Helper()
	ctx := context.Background()
	created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: f.Owner, Title: "Reply parameters", Megagroup: true, MemberUserIDs: []int64{f.Member}, Date: 1700020000})
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: f.Owner, ChannelID: created.Channel.ID, RandomID: 45001, Message: "😀hello", Date: 1700020001})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.SendChannelMessageRequest{UserID: f.Owner, ChannelID: created.Channel.ID, RandomID: 45002, Message: "quote", ReplyTo: &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}, MessageID: source.Message.ID, QuoteText: "hello", QuoteOffset: 2}, Date: 1700020002}
	result, err := f.Channels.SendChannelMessage(ctx, req)
	if err != nil || result.Message.ReplyTo == nil || result.Message.ReplyTo.QuoteOffset != 2 {
		t.Fatalf("channel quote=%+v %v", result, err)
	}
	req.RandomID = 45003
	req.ReplyTo.QuoteOffset = 1
	if _, err := f.Channels.SendChannelMessage(ctx, req); err == nil {
		t.Fatal("channel split UTF16 quote accepted")
	}
	req.RandomID = 45004
	req.ReplyTo = &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}, MessageID: result.Message.ID + 1, AllowMissing: true}
	fallback, err := f.Channels.SendChannelMessage(ctx, req)
	if err != nil || fallback.Message.ReplyTo != nil {
		t.Fatalf("channel missing fallback=%+v %v", fallback, err)
	}
	replay, err := f.Channels.SendChannelMessage(ctx, req)
	if err != nil || !replay.Duplicate || replay.Message.ID != fallback.Message.ID || replay.Message.ReplyTo != nil {
		t.Fatalf("channel fallback replay=%+v %v", replay, err)
	}
}
