package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type BotCommerceFixture struct {
	Commerce                              store.BotCommerce
	Payments                              store.BotInvoicePayments
	Stars                                 store.StarsStore
	Channels                              store.ChannelStore
	BotID, BuyerID, StrangerID, ChannelID int64
}

func BotCommerce(t *testing.T, newFixture func(*testing.T) BotCommerceFixture) {
	const date = 1800000000
	offer := func(botID int64, period int) domain.InvoiceLink {
		return domain.InvoiceLink{Slug: fmt.Sprintf("%048x", botID), BotID: botID, OwnerID: botID, Title: "Access", Description: "Paid access", Payload: "order-1", Amount: 100, SubscriptionPeriod: period, Date: date}
	}
	fund := func(t *testing.T, f BotCommerceFixture, amount int64) {
		t.Helper()
		if _, err := f.Stars.Credit(context.Background(), f.BuyerID, amount, domain.StarsReasonTopup, domain.Peer{}, date, "Funding", ""); err != nil {
			t.Fatal(err)
		}
	}
	balance := func(t *testing.T, f BotCommerceFixture) int64 {
		t.Helper()
		v, err := f.Stars.GetBalance(context.Background(), f.BuyerID)
		if err != nil {
			t.Fatal(err)
		}
		return v.Balance
	}
	t.Run("InvoiceReplayRefundAndWrongPrincipal", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		fund(t, f, 500)
		link := offer(f.BotID, 0)
		if err := f.Commerce.CreateInvoiceLink(ctx, link); err != nil {
			t.Fatal(err)
		}
		resolved, err := f.Commerce.InvoiceLink(ctx, link.Slug)
		if err != nil || resolved.Payload != "order-1" || resolved.Amount != 100 {
			t.Fatalf("offer=%+v err=%v", resolved, err)
		}
		invoice, err := f.Commerce.CreateInvoiceCheckout(ctx, link.Slug, f.BuyerID, date)
		if err != nil {
			t.Fatal(err)
		}
		if invoice.MessageID != 0 {
			t.Fatal("invoice link checkout fabricated a chat invoice message")
		}
		if _, err = f.Commerce.InvoiceCheckout(ctx, link.Slug, f.StrangerID, invoice.ID); err == nil {
			t.Fatal("checkout leaked to another principal")
		}
		if _, err = f.Payments.PayBotInvoice(ctx, invoice.ID, f.StrangerID, date); err == nil {
			t.Fatal("wrong buyer paid")
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := f.Payments.PayBotInvoice(ctx, invoice.ID, f.BuyerID, date)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := balance(t, f); got != 400 {
			t.Fatalf("buyer=%d", got)
		}
		wallet, err := f.Commerce.BotStarsBalance(ctx, f.BotID)
		if err != nil || wallet != 100 {
			t.Fatalf("wallet=%d %v", wallet, err)
		}
		rows, err := f.Commerce.BotStarTransactions(ctx, f.BotID, 0, 100)
		charge := fmt.Sprintf("botinv-%d", invoice.ID)
		if err != nil || len(rows) != 1 || rows[0].ID != charge || rows[0].Amount != 100 || rows[0].Peer.ID != f.BuyerID || rows[0].Payload != link.Payload {
			t.Fatalf("history=%+v %v", rows, err)
		}
		if err = f.Payments.RefundBotInvoice(ctx, f.BotID, f.StrangerID, charge, date+1); err == nil {
			t.Fatal("refund redirected")
		}
		for range 2 {
			if err = f.Payments.RefundBotInvoice(ctx, f.BotID, f.BuyerID, charge, date+1); err != nil {
				t.Fatal(err)
			}
		}
		rows, err = f.Commerce.BotStarTransactions(ctx, f.BotID, 0, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != charge || rows[0].Amount != -100 {
			t.Fatalf("refund history=%+v %v", rows, err)
		}
		if got := balance(t, f); got != 500 {
			t.Fatalf("refund buyer=%d", got)
		}
		wallet, _ = f.Commerce.BotStarsBalance(ctx, f.BotID)
		if wallet != 0 {
			t.Fatalf("refund wallet=%d", wallet)
		}
		if _, err = f.Payments.PayBotInvoice(ctx, invoice.ID, f.BuyerID, date+2); err == nil {
			t.Fatal("refunded invoice paid again")
		}
	})
	t.Run("UnfundedInvoiceAndFailedMediaDoNotDebit", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		fund(t, f, 50)
		link := offer(f.BotID, 0)
		if err := f.Commerce.CreateInvoiceLink(ctx, link); err != nil {
			t.Fatal(err)
		}
		invoice, err := f.Commerce.CreateInvoiceCheckout(ctx, link.Slug, f.BuyerID, date)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Payments.PayBotInvoice(ctx, invoice.ID, f.BuyerID, date); !errors.Is(err, domain.ErrStarsInsufficient) {
			t.Fatalf("pay=%v", err)
		}
		saved, err := f.Commerce.InvoiceCheckout(ctx, link.Slug, f.BuyerID, invoice.ID)
		if err != nil || saved.Paid {
			t.Fatalf("failed invoice=%+v %v", saved, err)
		}
		if _, err = f.Commerce.PurchasePaidMedia(ctx, "missing", f.BuyerID, date); err == nil {
			t.Fatal("missing media purchased")
		}
		if got := balance(t, f); got != 50 {
			t.Fatalf("failed payment buyer=%d", got)
		}
		wallet, _ := f.Commerce.BotStarsBalance(ctx, f.BotID)
		if wallet != 0 {
			t.Fatalf("failed payment wallet=%d", wallet)
		}
	})
	t.Run("RecurringCancelRestoreConcurrentRenewalAndExpiry", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		fund(t, f, 250)
		link := offer(f.BotID, domain.StarSubscriptionPeriod)
		if err := f.Commerce.CreateInvoiceLink(ctx, link); err != nil {
			t.Fatal(err)
		}
		invoice, err := f.Commerce.CreateInvoiceCheckout(ctx, link.Slug, f.BuyerID, date)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Payments.PayBotInvoice(ctx, invoice.ID, f.BuyerID, date); err != nil {
			t.Fatal(err)
		}
		subs, err := f.Commerce.UserSubscriptions(ctx, f.BuyerID)
		if err != nil || len(subs) != 1 || subs[0].Until != date+domain.StarSubscriptionPeriod {
			t.Fatalf("subscriptions=%+v %v", subs, err)
		}
		charge := subs[0].ChargeID
		if err = f.Commerce.EditUserSubscription(ctx, f.BotID, f.StrangerID, charge, true); err == nil {
			t.Fatal("another user subscription edited")
		}
		if err = f.Commerce.EditUserSubscription(ctx, f.BotID, f.BuyerID, charge, true); err != nil {
			t.Fatal(err)
		}
		if err = f.Commerce.ChangeUserSubscription(ctx, f.BuyerID, subs[0].ID, false, date); err == nil {
			t.Fatal("user bypassed bot cancellation")
		}
		if got := balance(t, f); got != 150 {
			t.Fatalf("cancel changed balance=%d", got)
		}
		if err = f.Commerce.EditUserSubscription(ctx, f.BotID, f.BuyerID, charge, false); err != nil {
			t.Fatal(err)
		}
		subs, _ = f.Commerce.UserSubscriptions(ctx, f.BuyerID)
		if !subs[0].UserCanceled {
			t.Fatal("bot restoration reactivated without user consent")
		}
		if err = f.Commerce.ChangeUserSubscription(ctx, f.BuyerID, subs[0].ID, false, date); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- f.Commerce.ProcessSubscriptions(ctx, date+domain.StarSubscriptionPeriod, 100)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := balance(t, f); got != 50 {
			t.Fatalf("renewal charged multiple times=%d", got)
		}
		subs, _ = f.Commerce.UserSubscriptions(ctx, f.BuyerID)
		if len(subs) != 1 || subs[0].Until != date+2*domain.StarSubscriptionPeriod {
			t.Fatalf("renewal=%+v", subs)
		}
		if err = f.Commerce.ProcessSubscriptions(ctx, date+2*domain.StarSubscriptionPeriod, 100); err != nil {
			t.Fatal(err)
		}
		subs, _ = f.Commerce.UserSubscriptions(ctx, f.BuyerID)
		if subs[0].Active || balance(t, f) != 50 {
			t.Fatalf("unfunded expiry=%+v", subs)
		}
	})
	t.Run("PaidMediaPreviewDownloadGateAndOneShotUnlock", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		fund(t, f, 100)
		bundle := domain.PaidMedia{ID: "paid-photo", BotID: f.BotID, OwnerID: f.BotID, ChatID: f.BuyerID, Price: 75, Payload: "media-order", Items: []*domain.MessageMedia{{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 98765, AccessHash: 123, FileReference: []byte("secret"), Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 640, H: 480}}}}}}
		if err := f.Commerce.SavePaidMedia(ctx, bundle); err != nil {
			t.Fatal(err)
		}
		preview, err := f.Commerce.PaidMedia(ctx, bundle.ID, f.BuyerID)
		if err != nil || preview.Unlocked || preview.Items[0].Photo.ID != 0 || len(preview.Items[0].Photo.FileReference) != 0 {
			t.Fatalf("preview leaked capability=%+v %v", preview, err)
		}
		key := "photo:98765:x"
		if allowed, err := f.Commerce.CanReadPaidFile(ctx, f.BuyerID, key); err != nil || allowed {
			t.Fatalf("unpaid read=%v %v", allowed, err)
		}
		if _, err = f.Commerce.PurchasePaidMedia(ctx, bundle.ID, f.StrangerID, date); err == nil {
			t.Fatal("private media purchased by unrelated user")
		}
		for range 2 {
			_, err = f.Commerce.PurchasePaidMedia(ctx, bundle.ID, f.BuyerID, date)
			if err != nil {
				t.Fatal(err)
			}
		}
		unlocked, err := f.Commerce.PaidMedia(ctx, bundle.ID, f.BuyerID)
		if err != nil || !unlocked.Unlocked || unlocked.Items[0].Photo.ID != 98765 {
			t.Fatalf("unlock=%+v %v", unlocked, err)
		}
		if allowed, _ := f.Commerce.CanReadPaidFile(ctx, f.BuyerID, key); !allowed {
			t.Fatal("paid file remained locked")
		}
		if allowed, _ := f.Commerce.CanReadPaidFile(ctx, f.StrangerID, key); allowed {
			t.Fatal("purchase unlocked file globally")
		}
		if got := balance(t, f); got != 25 {
			t.Fatalf("media debit=%d", got)
		}
		wallet, _ := f.Commerce.BotStarsBalance(ctx, f.BotID)
		if wallet != 75 {
			t.Fatalf("media proceeds=%d", wallet)
		}
	})
	t.Run("ChannelSubscriptionRequiresPaymentThenExpiresMembership", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		fund(t, f, 300)
		invite, err := f.Commerce.CreateSubscriptionInvite(ctx, f.BotID, f.ChannelID, 50, "Monthly", date)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: f.BuyerID, Hash: invite.Hash, Date: date}); err == nil {
			t.Fatal("subscription invite joined without payment")
		}
		offer, err := f.Commerce.SubscriptionInvite(ctx, invite.Hash)
		if err != nil {
			t.Fatal(err)
		}
		invoice, err := f.Commerce.CreateInvoiceCheckout(ctx, offer.Slug, f.BuyerID, date)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Payments.PayBotInvoice(ctx, invoice.ID, f.BuyerID, date); err != nil {
			t.Fatal(err)
		}
		member, err := f.Channels.GetParticipant(ctx, f.BotID, f.ChannelID, f.BuyerID)
		if err != nil || member.Status != domain.ChannelMemberActive {
			t.Fatalf("paid member=%+v %v", member, err)
		}
		subs, err := f.Commerce.UserSubscriptions(ctx, f.BuyerID)
		if err != nil || len(subs) != 1 {
			t.Fatalf("channel sub=%+v %v", subs, err)
		}
		if _, err = f.Channels.LeaveChannel(ctx, f.ChannelID, f.BuyerID, date+1); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Commerce.FulfillSubscription(ctx, f.BuyerID, subs[0].ID, date+2); err != nil {
			t.Fatal(err)
		}
		if got := balance(t, f); got != 250 {
			t.Fatalf("refulfill charged again=%d", got)
		}
		if err = f.Commerce.EditUserSubscription(ctx, f.BotID, f.BuyerID, subs[0].ChargeID, true); err != nil {
			t.Fatal(err)
		}
		if err = f.Commerce.ProcessSubscriptions(ctx, date+domain.StarSubscriptionPeriod-1, 100); err != nil {
			t.Fatal(err)
		}
		member, err = f.Channels.GetParticipant(ctx, f.BotID, f.ChannelID, f.BuyerID)
		if err != nil || member.Status != domain.ChannelMemberActive {
			t.Fatal("cancel removed paid membership early")
		}
		if err = f.Commerce.ProcessSubscriptions(ctx, date+domain.StarSubscriptionPeriod, 100); err != nil {
			t.Fatal(err)
		}
		member, err = f.Channels.GetParticipant(ctx, f.BotID, f.ChannelID, f.BuyerID)
		if err == nil && member.Status == domain.ChannelMemberActive {
			t.Fatal("expired member retained access")
		}
		if got := balance(t, f); got != 250 {
			t.Fatalf("canceled subscription charged=%d", got)
		}
		wallet, _ := f.Commerce.BotStarsBalance(ctx, f.BotID)
		if wallet != 0 {
			t.Fatalf("channel proceeds entered bot wallet=%d", wallet)
		}
	})
	t.Run("BusinessTransferConservesFundsAndFailsAtomically", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t)
		fund(t, f, 1000)
		var wg sync.WaitGroup
		errs := make(chan error, 10)
		for range 10 {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- f.Commerce.BusinessTransfer(ctx, f.BuyerID, f.BotID, 75, date) }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		wallet, err := f.Commerce.BotStarsBalance(ctx, f.BotID)
		if err != nil || wallet != 750 || balance(t, f) != 250 {
			t.Fatalf("transfer conservation wallet=%d %v", wallet, err)
		}
		if err = f.Commerce.BusinessTransfer(ctx, f.BuyerID, f.BotID, 251, date); !errors.Is(err, domain.ErrStarsInsufficient) {
			t.Fatalf("overdraft=%v", err)
		}
		after, _ := f.Commerce.BotStarsBalance(ctx, f.BotID)
		if after != 750 || balance(t, f) != 250 {
			t.Fatal("failed transfer partially mutated funds")
		}
		personal, err := f.Stars.ListTransactions(ctx, f.BuyerID, domain.StarsTransactionQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		outgoing := int64(0)
		for _, row := range personal.Transactions {
			if row.Reason == domain.StarsTransactionReason("business_transfer") {
				if row.Peer.ID != f.BotID {
					t.Fatal("wrong debit counterparty")
				}
				outgoing += row.Amount
			}
		}
		incoming, err := f.Commerce.BotStarTransactions(ctx, f.BotID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		sum := int64(0)
		for _, row := range incoming {
			if row.Peer.ID != f.BuyerID {
				t.Fatal("wrong credit counterparty")
			}
			sum += row.Amount
		}
		if outgoing != -750 || sum != 750 {
			t.Fatalf("ledger mismatch=%d/%d", outgoing, sum)
		}
	})
}
