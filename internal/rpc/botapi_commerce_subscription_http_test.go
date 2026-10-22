package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func TestBotAPICommerceSubscriptionInviteAndCancellationHTTP(t *testing.T) {
	f := newCommerceRPCFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.r.deps.Bots, f.r.deps.Users.(botapi.UsersService), nil, nil, f.r, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	call := func(method string, fields map[string]any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, req)
		var out map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %s: %v", method, response.Body, err)
		}
		return out
	}
	channel, err := f.r.deps.Channels.CreateChannel(ctx, f.bot.ID, domain.CreateChannelRequest{Title: "Paid channel", Broadcast: true, RandomID: 1, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	chatID := -botAPIChannelChatIDBase - channel.Channel.ID
	peer := &tg.InputPeerChannel{ChannelID: channel.Channel.ID, AccessHash: channel.Channel.AccessHash}
	botCtx := WithUserID(ctx, f.bot.ID)
	buyerCtx := WithUserID(ctx, f.buyer.ID)

	// A real channel owned by another account is not an invitation capability.
	foreign, err := f.r.deps.Channels.CreateChannel(ctx, f.buyer.ID, domain.CreateChannelRequest{Title: "Not the bot's channel", Broadcast: true, RandomID: 2, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	requireChatInviteError(t, call("createChatSubscriptionInviteLink", map[string]any{"chat_id": -botAPIChannelChatIDBase - foreign.Channel.ID, "name": "unauthorized", "subscription_period": domain.StarSubscriptionPeriod, "subscription_price": 40}))

	created := requireChatInviteOK(t, call("createChatSubscriptionInviteLink", map[string]any{"chat_id": chatID, "name": "Monthly", "subscription_period": domain.StarSubscriptionPeriod, "subscription_price": 40})).(map[string]any)
	link := created["invite_link"].(string)
	hash, err := channelInviteHashFromLink(link)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := f.commerce.SubscriptionInvite(ctx, hash)
	if err != nil || offer.BotID != f.bot.ID || offer.ChannelID != channel.Channel.ID || offer.Amount != 40 || offer.SubscriptionPeriod != domain.StarSubscriptionPeriod {
		t.Fatalf("persisted paid offer=%+v: %v", offer, err)
	}
	assertInvite := func(title string) {
		t.Helper()
		result, err := f.r.onMessagesGetExportedChatInvite(botCtx, &tg.MessagesGetExportedChatInviteRequest{Peer: peer, Link: link})
		if err != nil {
			t.Fatal(err)
		}
		invite := result.(*tg.MessagesExportedChatInvite).Invite.(*tg.ChatInviteExported)
		pricing, present := invite.GetSubscriptionPricing()
		if invite.Link != link || invite.AdminID != f.bot.ID || invite.Title != title || !present || pricing.Amount != 40 || pricing.Period != domain.StarSubscriptionPeriod {
			t.Fatalf("native paid invite=%+v", invite)
		}
	}
	assertInvite("Monthly")
	requireChatInviteOK(t, call("editChatSubscriptionInviteLink", map[string]any{"chat_id": chatID, "invite_link": link, "name": "Renamed"}))
	assertInvite("Renamed")
	afterEdit, err := f.commerce.SubscriptionInvite(ctx, hash)
	if err != nil || afterEdit != offer {
		t.Fatalf("name edit changed the financial offer: before=%+v after=%+v: %v", offer, afterEdit, err)
	}
	if _, err := f.r.onMessagesImportChatInvite(buyerCtx, hash); err == nil {
		t.Fatal("paid invite granted membership before payment")
	}

	// Channel subscription invoices use native Stars checkout, without a bot
	// pre-checkout query (unlike ordinary bot invoices).
	invoice := &tg.InputInvoiceChatInviteSubscription{Hash: hash}
	form, err := f.r.onPaymentsGetPaymentForm(buyerCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatal(err)
	}
	payment := &tg.PaymentsSendStarsFormRequest{Invoice: invoice, FormID: form.(*tg.PaymentsPaymentFormStars).FormID}
	for range 2 {
		if _, err := f.r.onPaymentsSendStarsForm(buyerCtx, payment); err != nil {
			t.Fatal(err)
		}
	}
	subs, err := f.commerce.UserSubscriptions(ctx, f.buyer.ID)
	if err != nil || len(subs) != 1 {
		t.Fatalf("paid subscriptions=%+v: %v", subs, err)
	}
	paid := subs[0]
	if !paid.Active || paid.Canceled || paid.UserCanceled || paid.ChargeID == "" || paid.Amount != 40 || paid.Period != domain.StarSubscriptionPeriod || paid.ChannelID != channel.Channel.ID || paid.Until <= offer.Date {
		t.Fatalf("paid entitlement=%+v", paid)
	}
	assertState := func(botCanceled, userCanceled, active bool) {
		t.Helper()
		rows, err := f.commerce.UserSubscriptions(ctx, f.buyer.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("subscriptions=%+v: %v", rows, err)
		}
		got := rows[0]
		if got.ID != paid.ID || got.ChargeID != paid.ChargeID || got.Until != paid.Until || got.Active != active || got.Canceled != botCanceled || got.UserCanceled != userCanceled {
			t.Fatalf("subscription changed paid access or cancellation state: %+v", got)
		}
		native, err := f.r.onPaymentsGetStarsSubscriptions(buyerCtx, &tg.PaymentsGetStarsSubscriptionsRequest{Peer: &tg.InputPeerSelf{}})
		if err != nil || len(native.Subscriptions) != 1 {
			t.Fatalf("native subscriptions=%+v: %v", native, err)
		}
		projected := native.Subscriptions[0]
		if projected.ID != paid.ID || projected.UntilDate != paid.Until || projected.BotCanceled != botCanceled || projected.Canceled != (botCanceled || userCanceled) || projected.Pricing.Amount != 40 || projected.Pricing.Period != domain.StarSubscriptionPeriod {
			t.Fatalf("native recurring entitlement=%+v", projected)
		}
		balance, err := f.r.deps.Stars.GetBalance(ctx, f.buyer.ID)
		if err != nil || balance.Balance != 60 {
			t.Fatalf("checkout replay/cancellation charged buyer again: %+v: %v", balance, err)
		}
		wallet, err := f.r.BotAPIGetMyStarBalance(ctx, f.bot.ID)
		if err != nil || wallet.Amount != 0 {
			t.Fatalf("channel proceeds leaked into bot wallet: %+v: %v", wallet, err)
		}
		member, err := f.r.deps.Channels.GetParticipant(ctx, f.bot.ID, channel.Channel.ID, f.buyer.ID)
		if active && (err != nil || member.Status != domain.ChannelMemberActive) {
			t.Fatalf("paid access removed early: %+v: %v", member, err)
		}
		if !active && err == nil && member.Status == domain.ChannelMemberActive {
			t.Fatal("expired canceled subscription retained channel access")
		}
	}
	assertState(false, false, true)
	fields := map[string]any{"user_id": f.bot.ID, "telegram_payment_charge_id": paid.ChargeID, "is_canceled": true}
	requireChatInviteError(t, call("editUserStarSubscription", fields))
	assertState(false, false, true)
	fields["user_id"] = f.buyer.ID
	requireChatInviteOK(t, call("editUserStarSubscription", fields))
	assertState(true, true, true)
	fields["is_canceled"] = false
	requireChatInviteOK(t, call("editUserStarSubscription", fields))
	// Restoration removes the bot veto, but must not silently restore the
	// customer's consent to recurring debits or extend the paid-through date.
	assertState(false, true, true)
	if err := f.r.ProcessBotSubscriptions(ctx, paid.Until-1, 100); err != nil {
		t.Fatal(err)
	}
	assertState(false, true, true)
	if err := f.r.ProcessBotSubscriptions(ctx, paid.Until, 100); err != nil {
		t.Fatal(err)
	}
	assertState(false, true, false)
}
