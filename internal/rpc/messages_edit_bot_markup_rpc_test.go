package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func callbackKeyboard(text, data string) *tg.ReplyInlineMarkup {
	return &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{
		Buttons: []tg.KeyboardInlineButton{{Text: text, Type: &tg.InlineButtonTypeCallback{Data: []byte(data)}}},
	}}}
}

// bot 的 messages.editMessage 按官方语义处理 reply_markup：不带 = 移除内联键盘；
// 只带 reply_markup（或什么都不带）= markup-only 编辑，保留原正文与实体。
// 对应 Bot API editMessageText（不传 reply_markup）与 editMessageReplyMarkup 三种写法。
func TestBotEditMessageReplyMarkupSemantics(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	if _, err := f.router.onMessagesSendMessage(botCtx, &tg.MessagesSendMessageRequest{
		Peer:        inputPeerUser(f.owner),
		Message:     "pick one",
		Entities:    []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 4}},
		RandomID:    7301,
		ReplyMarkup: callbackKeyboard("A", "a"),
	}); err != nil {
		t.Fatalf("bot send with keyboard: %v", err)
	}
	latest := func(t *testing.T) domain.Message {
		t.Helper()
		history, err := f.router.deps.Messages.GetHistory(ctx, f.owner.ID, domain.MessageFilter{
			HasPeer: true,
			Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID},
			Limit:   1,
		})
		if err != nil || len(history.Messages) != 1 {
			t.Fatalf("owner history len = %d err=%v, want 1,nil", len(history.Messages), err)
		}
		return history.Messages[0]
	}
	msgID := latest(t).ID
	edit := func(t *testing.T, req *tg.MessagesEditMessageRequest) {
		t.Helper()
		req.Peer, req.ID = inputPeerUser(f.owner), msgID
		if _, err := f.router.onMessagesEditMessage(botCtx, req); err != nil {
			t.Fatalf("bot edit: %v", err)
		}
	}
	assertText := func(t *testing.T, msg domain.Message, want string) {
		t.Helper()
		if msg.Body != want || len(msg.Entities) != 1 || msg.Entities[0].Type != domain.MessageEntityBold {
			t.Fatalf("message body/entities = %q/%+v, want %q with bold", msg.Body, msg.Entities, want)
		}
	}

	t.Run("text edit without reply_markup removes keyboard", func(t *testing.T) {
		req := &tg.MessagesEditMessageRequest{}
		req.SetMessage("pick two")
		req.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 4}})
		edit(t, req)
		msg := latest(t)
		assertText(t, msg, "pick two")
		if !msg.ReplyMarkup.IsZero() {
			t.Fatalf("markup = %+v, want removed", msg.ReplyMarkup)
		}
	})

	t.Run("markup-only edit sets new keyboard and keeps text", func(t *testing.T) {
		req := &tg.MessagesEditMessageRequest{}
		req.SetReplyMarkup(callbackKeyboard("B", "b"))
		edit(t, req)
		msg := latest(t)
		assertText(t, msg, "pick two")
		assertDomainInlineReplyMarkup(t, msg.ReplyMarkup, "B", []byte("b"))
	})

	t.Run("markup-only edit with empty keyboard removes it", func(t *testing.T) {
		req := &tg.MessagesEditMessageRequest{}
		req.SetReplyMarkup(&tg.ReplyInlineMarkup{})
		edit(t, req)
		msg := latest(t)
		assertText(t, msg, "pick two")
		if !msg.ReplyMarkup.IsZero() {
			t.Fatalf("markup = %+v, want removed", msg.ReplyMarkup)
		}
	})

	t.Run("edit with nothing removes keyboard and keeps text", func(t *testing.T) {
		req := &tg.MessagesEditMessageRequest{}
		req.SetReplyMarkup(callbackKeyboard("C", "c"))
		edit(t, req)
		edit(t, &tg.MessagesEditMessageRequest{})
		msg := latest(t)
		assertText(t, msg, "pick two")
		if !msg.ReplyMarkup.IsZero() {
			t.Fatalf("markup = %+v, want removed", msg.ReplyMarkup)
		}
	})

	t.Run("non-bot edit with nothing stays MESSAGE_EMPTY", func(t *testing.T) {
		if _, err := f.router.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
			Peer:     inputPeerUser(f.bot),
			Message:  "hi bot",
			RandomID: 7302,
		}); err != nil {
			t.Fatalf("owner send: %v", err)
		}
		own := latest(t)
		if own.Body != "hi bot" {
			t.Fatalf("latest = %q, want owner's message", own.Body)
		}
		_, err := f.router.onMessagesEditMessage(ownerCtx, &tg.MessagesEditMessageRequest{Peer: inputPeerUser(f.bot), ID: own.ID})
		if !tgerr.Is(err, "MESSAGE_EMPTY") {
			t.Fatalf("user empty edit err = %v, want MESSAGE_EMPTY", err)
		}
	})
}
