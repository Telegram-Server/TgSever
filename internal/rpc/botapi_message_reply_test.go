package rpc

import (
	"fmt"
	"telesrv/internal/domain"
	"testing"
)

func TestBotAPIReplyParametersCrossChatQuoteAndPrivacy(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	source, err := f.router.BotAPISendMessage(f.ctx, f.bot.ID, f.owner.ID, "😀hello", []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 2, Length: 5}}, nil, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	parameters := &domain.BotAPIReplyParameters{MessageID: source.ID, ChatID: fmt.Sprint(f.owner.ID), Quote: "hello", QuoteEntities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 5}}}
	sent, err := f.router.BotAPISendMessageWithOptions(f.ctx, f.bot.ID, f.member.ID, "quoted", nil, nil, false, false, 0, domain.BotAPIMessageOptions{ReplyParameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	if sent.ReplyTo == nil || sent.ReplyTo.External == nil || sent.ReplyTo.QuoteOffset != 2 || sent.ReplyTo.QuoteText != "hello" {
		t.Fatalf("canonical cross-chat quote=%+v", sent.ReplyTo)
	}
	history, err := f.router.botAPIReloadMessage(f.ctx, f.bot.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}, sent.ID)
	if err != nil || history.ReplyTo == nil || history.ReplyTo.External == nil || history.ReplyTo.QuoteOffset != 2 {
		t.Fatalf("persisted external quote=%+v %v", history, err)
	}
	parameters.QuoteEntities = nil
	if _, err := f.router.BotAPISendMessageWithOptions(f.ctx, f.bot.ID, f.member.ID, "bad formatting", nil, nil, false, false, 0, domain.BotAPIMessageOptions{ReplyParameters: parameters}); err == nil {
		t.Fatal("quote formatting mismatch accepted")
	}
	protected, err := f.messages.SendPrivateText(f.ctx, f.bot.ID, domain.SendPrivateTextRequest{SenderUserID: f.bot.ID, RecipientUserID: f.owner.ID, RandomID: 46001, Message: "protected", NoForwards: true, Date: int(f.router.clock.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.BotAPISendMessageWithOptions(f.ctx, f.bot.ID, f.member.ID, "leak", nil, nil, false, false, 0, domain.BotAPIMessageOptions{ReplyParameters: &domain.BotAPIReplyParameters{MessageID: protected.SenderMessage.ID, ChatID: fmt.Sprint(f.owner.ID)}}); err == nil {
		t.Fatal("protected external reply leaked")
	}
	if _, err := f.router.BotAPISendMessageWithOptions(f.ctx, f.bot.ID, f.member.ID, "missing source", nil, nil, false, false, 0, domain.BotAPIMessageOptions{ReplyParameters: &domain.BotAPIReplyParameters{MessageID: 999999, ChatID: fmt.Sprint(f.owner.ID), AllowSendingWithoutReply: true}}); err == nil {
		t.Fatal("cross-chat allow-missing accepted")
	}
}
