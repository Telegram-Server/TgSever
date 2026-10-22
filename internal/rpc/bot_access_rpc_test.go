package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
)

func TestRestrictedBotRejectsInteractionsAndPreviouslyIssuedInlineGrant(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	visitorCtx := WithUserID(ctx, f.peer.ID)
	botPeer := &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	queryID, _ := f.router.inlines.register(f.router.clock.Now(), f.bot.ID, f.peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID})
	if ok, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.bot.ID), &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID, CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{inlineArticleResult("existing-grant", "issued before restriction")},
	}); err != nil || !ok {
		t.Fatalf("issue inline grant: ok=%v err=%v", ok, err)
	}
	data := []byte("confirm")
	sent, err := f.router.deps.Messages.SendPrivateText(ctx, f.bot.ID, domain.SendPrivateTextRequest{
		SenderUserID: f.bot.ID, RecipientUserID: f.peer.ID, RandomID: 98001,
		Message: "existing keyboard", Date: int(f.router.clock.Now().Unix()),
		ReplyMarkup: &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{
			Type: domain.MarkupButtonCallback, Text: "Confirm", Data: data,
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.bots.SetBotAccessSettings(ctx, f.owner.ID, f.bot.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		call func() error
	}{
		{"start", func() error {
			_, err := f.router.onMessagesStartBot(visitorCtx, &tg.MessagesStartBotRequest{Bot: inputUser(f.bot), Peer: botPeer, RandomID: 98002})
			return err
		}},
		{"inline query", func() error {
			_, err := f.router.onMessagesGetInlineBotResults(visitorCtx, &tg.MessagesGetInlineBotResultsRequest{Bot: inputUser(f.bot), Peer: &tg.InputPeerEmpty{}, Query: "secret"})
			return err
		}},
		{"issued inline grant", func() error {
			_, err := f.router.onMessagesSendInlineBotResult(visitorCtx, &tg.MessagesSendInlineBotResultRequest{
				Peer: &tg.InputPeerUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash}, QueryID: queryID, ID: "existing-grant", RandomID: 98003,
			})
			return err
		}},
		{"existing callback", func() error {
			req := &tg.MessagesGetBotCallbackAnswerRequest{Peer: botPeer, MsgID: sent.RecipientMessage.ID}
			req.SetData(data)
			_, err := f.router.onMessagesGetBotCallbackAnswer(visitorCtx, req)
			return err
		}},
		{"mini app", func() error {
			req := &tg.MessagesRequestSimpleWebViewRequest{Bot: inputUser(f.bot)}
			req.SetURL("https://example.com/app")
			_, err := f.router.onMessagesRequestSimpleWebView(visitorCtx, req)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !tgerr.Is(err, "BOT_ACCESS_DENIED") {
				t.Fatalf("restricted interaction = %v, want BOT_ACCESS_DENIED", err)
			}
		})
	}
	if err := f.bots.SetBotAccessSettings(ctx, f.owner.ID, f.bot.ID, true, []int64{f.peer.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.onMessagesStartBot(visitorCtx, &tg.MessagesStartBotRequest{Bot: inputUser(f.bot), Peer: botPeer, RandomID: 98004}); err != nil {
		t.Fatalf("start after live allowlist change: %v", err)
	}
}
