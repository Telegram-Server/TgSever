package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestBotAPIMonoforumContentPersistsSuggestionAndRichMarkup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	now := int(f.router.clock.Now().Unix())
	created, err := f.channels.CreateChannel(f.ctx, f.owner.ID, domain.CreateChannelRequest{Title: "Suggested content", Broadcast: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := f.channels.SetPaidMessagesPrice(f.ctx, f.owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: enabled.Channel.LinkedMonoforumID}
	options := domain.BotAPIMessageOptions{DirectMessagesTopicID: int(f.member.ID), SuggestedPost: &domain.SuggestedPost{ScheduleDate: now + 600, Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}}
	if _, err := f.router.botAPISendMonoforumContent(f.ctx, f.bot.ID, peer, "unauthorized", nil, nil, nil, nil, false, false, nil, options); err == nil {
		t.Fatal("non-manager bot wrote channel direct messages")
	}
	if _, err := f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{ChannelID: created.Channel.ID, MemberID: f.bot.ID, AdminRights: domain.ChannelAdminRights{ManageDirectMessages: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	markup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: "Offer", Data: []byte("offer")}}}}
	rich := &domain.MessageRichMessage{Blocks: []byte{0x15, 0xc4, 0xb5, 0x1c, 1, 0, 0, 0, 0x8a, 0x7e, 0x56, 0x13}}
	message, err := f.router.botAPISendMonoforumContent(f.ctx, f.bot.ID, peer, "", nil, nil, rich, markup, false, true, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if message.SavedPeer.ID != f.member.ID || message.SuggestedPost == nil || message.SuggestedPost.ScheduleDate != now+600 || message.SuggestedPost.Price.Amount != 10 || message.RichMessage.IsZero() || message.ReplyMarkup == nil || !message.NoForwards {
		t.Fatalf("stored suggested content=%+v", message)
	}
	history, err := f.channels.ListMonoforumHistory(f.ctx, domain.MonoforumHistoryFilter{MonoforumID: peer.ID, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range history.Messages {
		if item.ID != message.ID {
			continue
		}
		found = item.SuggestedPost != nil && item.RichMessage != nil && item.ReplyMarkup != nil && string(item.ReplyMarkup.Inline[0][0].Data) == "offer"
	}
	if !found {
		t.Fatal("subscriber history lost real suggestion/rich/markup")
	}
	options.SuggestedPost.ScheduleDate = now + domain.MaxSuggestedPostScheduleDelay + 1
	if _, err := f.router.botAPISendMonoforumContent(f.ctx, f.bot.ID, peer, "far future", nil, nil, nil, nil, false, false, nil, options); err == nil {
		t.Fatal("out-of-window suggestion accepted")
	}
	options.SuggestedPost = nil
	options.DirectMessagesTopicID = 0
	if _, err := f.router.botAPISendMonoforumContent(f.ctx, f.bot.ID, peer, "missing topic", nil, nil, nil, nil, false, false, nil, options); err == nil {
		t.Fatal("direct message without topic accepted")
	}
}
