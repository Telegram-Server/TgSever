package rpc

import (
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func botAPITestKeyboard(text string) *domain.MessageReplyMarkup {
	return &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{
		{Type: domain.MarkupButtonCallback, Text: text, Data: []byte(text)},
	}}}
}

// sendBotPhoto 让机器人往 chatID 发一张带 caption 和按钮的照片，作为编辑测试的目标。
func (f botAPIBridgeFixture) sendBotPhoto(t *testing.T, chatID int64, caption string) domain.Message {
	t.Helper()
	msg, err := f.router.BotAPISendMediaFiles(f.ctx, f.bot.ID, chatID, "photo", domain.BotAPIFileInput{FileName: "a.png", MimeType: "image/png", Bytes: []byte("png")}, domain.BotAPIFileInput{}, caption, nil, botAPITestKeyboard("Old"), false, 0)
	if err != nil {
		t.Fatalf("send photo: %v", err)
	}
	return msg
}

// sendOwnerText 让群主在群里发一条文本，作为"别人的消息"。
func (f botAPIBridgeFixture) sendOwnerText(t *testing.T) domain.Message {
	t.Helper()
	input, peer, err := f.router.botAPIInputPeer(f.ctx, f.owner.ID, f.chatID)
	if err != nil {
		t.Fatalf("owner input peer: %v", err)
	}
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendMessageRequest{Peer: input, Message: "owner text", RandomID: 4401})
	if err != nil {
		t.Fatalf("owner send: %v", err)
	}
	msgs, err := f.router.botAPIMessagesFromUpdates(f.ctx, f.owner.ID, peer, updates)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("owner message = %+v, %v", msgs, err)
	}
	return msgs[0]
}

func TestBotAPIEditMessageCaptionInGroupAndPrivate(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	for _, chatID := range []int64{f.chatID, f.member.ID} {
		sent := f.sendBotPhoto(t, chatID, "old caption")

		edited, err := f.router.BotAPIEditMessageCaption(f.ctx, f.bot.ID, chatID, sent.ID, "new caption",
			[]domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 3}}, false, nil)
		if err != nil {
			t.Fatalf("chat %d: BotAPIEditMessageCaption: %v", chatID, err)
		}
		if edited.ID != sent.ID || edited.Body != "new caption" || len(edited.Entities) != 1 || edited.EditDate == 0 ||
			edited.Media == nil || edited.Media.Photo == nil {
			t.Fatalf("chat %d: edited = %+v", chatID, edited)
		}
		// 未给 reply_markup 时保留原键盘。
		if edited.ReplyMarkup == nil || len(edited.ReplyMarkup.Inline) != 1 {
			t.Fatalf("chat %d: markup = %+v, want the original keyboard kept", chatID, edited.ReplyMarkup)
		}
	}
}

func TestBotAPIEditMessageReplyMarkupKeepsCaption(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sent := f.sendBotPhoto(t, f.chatID, "keep me")

	edited, err := f.router.BotAPIEditMessageReplyMarkup(f.ctx, f.bot.ID, f.chatID, sent.ID, botAPITestKeyboard("New"))
	if err != nil {
		t.Fatalf("BotAPIEditMessageReplyMarkup: %v", err)
	}
	if edited.Body != "keep me" || edited.Media == nil || edited.Media.Photo == nil || edited.ReplyMarkup == nil ||
		edited.ReplyMarkup.Inline[0][0].Text != "New" {
		t.Fatalf("edited = %+v markup = %+v", edited, edited.ReplyMarkup)
	}

	cleared, err := f.router.BotAPIEditMessageReplyMarkup(f.ctx, f.bot.ID, f.chatID, sent.ID, nil)
	if err != nil {
		t.Fatalf("clear markup: %v", err)
	}
	if cleared.Body != "keep me" || (cleared.ReplyMarkup != nil && !cleared.ReplyMarkup.IsZero()) {
		t.Fatalf("cleared = %+v markup = %+v", cleared, cleared.ReplyMarkup)
	}
}

// 机器人不能改别人的消息：改说明文字与改键盘都返回 MESSAGE_AUTHOR_REQUIRED。
func TestBotAPIEditRejectsOthersMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	owner := f.sendOwnerText(t)

	if _, err := f.router.BotAPIEditMessageCaption(f.ctx, f.bot.ID, f.chatID, owner.ID, "hijack", nil, false, nil); !containsErr(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("edit caption of owner's message err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}
	if _, err := f.router.BotAPIEditMessageReplyMarkup(f.ctx, f.bot.ID, f.chatID, owner.ID, botAPITestKeyboard("X")); !containsErr(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("edit keyboard of owner's message err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}
}

func TestBotAPIEditMessageMediaReplacesPhoto(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sent := f.sendBotPhoto(t, f.chatID, "old")
	item := BotAPIInputMediaItem{Kind: "document", File: domain.BotAPIFileInput{Bytes: []byte("pdf"), FileName: "a.pdf", MimeType: "application/pdf"}, Caption: "a document"}

	edited, err := f.router.BotAPIEditMessageMedia(f.ctx, f.bot.ID, f.chatID, sent.ID, item, false, nil)
	if err != nil {
		t.Fatalf("BotAPIEditMessageMedia: %v", err)
	}
	if edited.ID != sent.ID || edited.Body != "a document" || edited.Media == nil || edited.Media.Document == nil ||
		edited.Media.Photo != nil || edited.EditDate == 0 {
		t.Fatalf("edited = %+v media = %+v", edited, edited.Media)
	}
	if edited.ReplyMarkup == nil || len(edited.ReplyMarkup.Inline) != 1 {
		t.Fatalf("markup = %+v, want the original keyboard kept", edited.ReplyMarkup)
	}
}

func TestBotAPIStopPollClosesAndReportsVotes(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sent, err := f.router.BotAPISendPoll(f.ctx, f.bot.ID, f.chatID, botAPITestPoll("Lunch?", "pizza", "sushi"), nil, "", nil, nil, false, false, 0)
	if err != nil {
		t.Fatalf("send poll: %v", err)
	}
	input, _, err := f.router.botAPIInputPeer(f.ctx, f.member.ID, f.chatID)
	if err != nil {
		t.Fatalf("member input peer: %v", err)
	}
	if _, err := f.router.onMessagesSendVote(WithUserID(f.ctx, f.member.ID), &tg.MessagesSendVoteRequest{
		Peer: input, MsgID: sent.ID, Options: [][]byte{sent.Media.Poll.Answers[1].Option},
	}); err != nil {
		t.Fatalf("member vote: %v", err)
	}

	poll, err := f.router.BotAPIStopPoll(f.ctx, f.bot.ID, f.chatID, sent.ID, true, botAPITestKeyboard("Results"))
	if err != nil {
		t.Fatalf("BotAPIStopPoll: %v", err)
	}
	if !poll.Closed || poll.ID != sent.Media.Poll.ID || poll.Results == nil || poll.Results.TotalVoters != 1 {
		t.Fatalf("poll = %+v results = %+v", poll, poll.Results)
	}
	reloaded, err := f.router.botAPIReloadMessage(f.ctx, f.bot.ID, sent.Peer, sent.ID)
	if err != nil || reloaded.ReplyMarkup == nil || reloaded.ReplyMarkup.Inline[0][0].Text != "Results" {
		t.Fatalf("reloaded = %+v, %v", reloaded, err)
	}

	if _, err := f.router.BotAPIStopPoll(f.ctx, f.bot.ID, f.chatID, sent.ID, false, nil); !containsErr(err, "MESSAGE_POLL_CLOSED") {
		t.Fatalf("second stop err = %v, want MESSAGE_POLL_CLOSED", err)
	}
}

func TestBotAPIEditInlineVariantsRejectUnknownInlineMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	unknown := domain.BotInlineMessageID{DCID: 2, OwnerID: f.member.ID, ID: 999, AccessHash: 1}

	if _, err := f.router.BotAPIEditInlineMessageCaption(f.ctx, f.bot.ID, unknown, "x", nil, false, nil); !containsErr(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("caption err = %v", err)
	}
	if _, err := f.router.BotAPIEditInlineMessageReplyMarkup(f.ctx, f.bot.ID, unknown, nil); !containsErr(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("markup err = %v", err)
	}
	source := f.sendBotPhoto(t, f.member.ID, "stored source")
	item := BotAPIInputMediaItem{Kind: "photo", File: domain.BotAPIFileInput{LocationKey: fmt.Sprintf("photo:%d:%s", source.Media.Photo.ID, source.Media.Photo.Sizes[0].Type)}}
	if _, err := f.router.BotAPIEditInlineMessageMedia(f.ctx, f.bot.ID, unknown, item, false, nil); !containsErr(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("media err = %v", err)
	}
}
