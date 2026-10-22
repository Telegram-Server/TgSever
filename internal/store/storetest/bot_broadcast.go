package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type BotBroadcastFixture struct {
	Ledger   store.BotBroadcastStore
	Reopen   func() store.BotBroadcastStore
	Messages interface {
		SendPrivateText(context.Context, domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
	}
	Commerce                    store.BotCommerce
	BotID, OwnerID, RecipientID int64
	Fund                        func(int64) error
}

// BotBroadcast runs the same money, delivery, replay and rolling-window contract
// on both stores. These are real messages and ledger mutations, not mocks.
func BotBroadcast(t *testing.T, f BotBroadcastFixture) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	balance := func(want domain.StarsAmount) {
		t.Helper()
		got, err := f.Ledger.BotStarsBalanceExact(ctx, f.BotID)
		if err != nil || got != want {
			t.Fatalf("balance=%+v err=%v want=%+v", got, err, want)
		}
	}
	if err := f.Ledger.SetBotPaidBroadcast(ctx, f.OwnerID, f.BotID, true, int(now.Unix())); !errors.Is(err, domain.ErrBotBroadcastEligibility) {
		t.Fatalf("unfunded enable=%v", err)
	}
	balance(domain.StarsAmount{})
	if err := f.Fund(11_000); err != nil {
		t.Fatal(err)
	}
	balance(domain.StarsAmount{Amount: 11_000})
	if err := f.Ledger.SetBotPaidBroadcast(ctx, f.RecipientID, f.BotID, true, int(now.Unix())); !errors.Is(err, domain.ErrBotNotFound) {
		t.Fatalf("non-owner enable=%v", err)
	}
	reserve := func(key string, paid bool) (domain.BotBroadcastReservation, error) {
		return f.Ledger.ReserveBotBroadcast(ctx, domain.BotBroadcastRequest{BotID: f.BotID, Key: key, AllowPaid: paid, Now: now})
	}
	send := func(n int, paid bool) (domain.Message, error) {
		key := fmt.Sprintf("send-%d", n)
		v, err := reserve(key, paid)
		if err != nil {
			return domain.Message{}, err
		}
		if v.State == "committed" {
			return *v.Message, nil
		}
		sendCtx := store.WithBotBroadcast(ctx, f.Ledger, v.Request, clock)
		result, err := f.Messages.SendPrivateText(sendCtx, domain.SendPrivateTextRequest{SenderUserID: f.BotID, RecipientUserID: f.RecipientID, RandomID: int64(n + 1), Message: fmt.Sprintf("broadcast %d", n), Date: int(now.Unix())})
		if err != nil {
			err = errors.Join(err, f.Ledger.ReleaseBotBroadcast(ctx, v.Request))
		}
		return result.SenderMessage, err
	}
	for i := range 30 {
		if _, err := send(i, false); err != nil {
			t.Fatalf("free send %d: %v", i, err)
		}
	}
	if _, err := reserve("over-free", false); !errors.Is(err, domain.ErrBotBroadcastLimit) {
		t.Fatalf("free quota=%v", err)
	}
	if _, err := reserve("disabled", true); !errors.Is(err, domain.ErrBotBroadcastNotEnabled) {
		t.Fatalf("disabled paid=%v", err)
	}
	balance(domain.StarsAmount{Amount: 11_000})
	if err := f.Ledger.SetBotPaidBroadcast(ctx, f.OwnerID, f.BotID, true, int(now.Unix())); err != nil {
		t.Fatal(err)
	}
	var first domain.Message
	for i := 30; i < 40; i++ {
		msg, err := send(i, true)
		if err != nil {
			t.Fatalf("paid send %d: %v", i, err)
		}
		if i == 30 {
			first = msg
		}
	}
	balance(domain.StarsAmount{Amount: 10_999}) // ten exact 0.1 fees equal one Star
	replay, err := send(30, true)
	if err != nil || replay.ID != first.ID || replay.UID != first.UID {
		t.Fatalf("replay=%+v err=%v original=%+v", replay, err, first)
	}
	balance(domain.StarsAmount{Amount: 10_999})
	failed, err := reserve("failed-send", true)
	if err != nil {
		t.Fatal(err)
	}
	balance(domain.StarsAmount{Amount: 10_998, Nanos: 900_000_000})
	_, err = f.Messages.SendPrivateText(store.WithBotBroadcast(ctx, f.Ledger, failed.Request, clock), domain.SendPrivateTextRequest{SenderUserID: f.BotID, RecipientUserID: f.RecipientID, RandomID: 900001, Message: "must not deliver", Date: int(now.Unix()), ReplyTo: &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.RecipientID}, MessageID: 2147483647}})
	if err == nil {
		t.Fatal("invalid reply was delivered")
	}
	if err := f.Ledger.ReleaseBotBroadcast(ctx, failed.Request); err != nil {
		t.Fatal(err)
	}
	balance(domain.StarsAmount{Amount: 10_999})
	blocked, err := reserve("blocked-recipient", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Messages.SendPrivateText(store.WithBotBroadcast(ctx, f.Ledger, blocked.Request, clock), domain.SendPrivateTextRequest{SenderUserID: f.BotID, RecipientUserID: f.RecipientID, RandomID: 900005, Message: "blocked cannot deliver", RecipientBlocked: true, Date: int(now.Unix())})
	if !errors.Is(err, domain.ErrBotBroadcastRecipientBlocked) {
		t.Fatalf("blocked broadcast=%v", err)
	}
	if err = f.Ledger.ReleaseBotBroadcast(ctx, blocked.Request); err != nil {
		t.Fatal(err)
	}
	balance(domain.StarsAmount{Amount: 10_999})
	rows, err := f.Commerce.BotStarTransactions(ctx, f.BotID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	feeCount := 0
	for _, row := range rows {
		if row.Reason == domain.StarsTransactionReason("paid_broadcast") {
			if row.Amount != 0 || row.Nanos != -100_000_000 {
				t.Fatalf("fractional journal=%+v", row)
			}
			feeCount++
		}
	}
	if feeCount != 10 {
		t.Fatalf("fee journal count=%d want10", feeCount)
	}
	// A reserved fee remains unavailable, but expiry safely releases it; the
	// old context can no longer commit a message after that release.
	expired, err := reserve("expired", true)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(domain.BotBroadcastReservationTTL + time.Second)
	trigger, err := reserve("after-expiry", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Ledger.ReleaseBotBroadcast(ctx, trigger.Request); err != nil {
		t.Fatal(err)
	}
	balance(domain.StarsAmount{Amount: 10_999})
	_, err = f.Messages.SendPrivateText(store.WithBotBroadcast(ctx, f.Ledger, expired.Request, clock), domain.SendPrivateTextRequest{SenderUserID: f.BotID, RecipientUserID: f.RecipientID, RandomID: 900002, Message: "expired cannot deliver", Date: int(now.Unix())})
	if err == nil {
		t.Fatal("expired reservation delivered a message")
	}
	retry, err := reserve("expired", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Ledger.ReleaseBotBroadcast(ctx, expired.Request); err != nil {
		t.Fatal(err)
	}
	_, err = f.Messages.SendPrivateText(store.WithBotBroadcast(ctx, f.Ledger, expired.Request, clock), domain.SendPrivateTextRequest{SenderUserID: f.BotID, RecipientUserID: f.RecipientID, RandomID: 900003, Message: "stale attempt cannot steal retry", Date: int(now.Unix())})
	if err == nil {
		t.Fatal("stale context committed the newer same-key reservation")
	}
	if _, err = f.Messages.SendPrivateText(store.WithBotBroadcast(ctx, f.Ledger, retry.Request, clock), domain.SendPrivateTextRequest{SenderUserID: f.BotID, RecipientUserID: f.RecipientID, RandomID: 900004, Message: "same-key retry delivers once", Date: int(now.Unix())}); err != nil {
		t.Fatalf("new reservation lost to old release: %v", err)
	}
	now = now.Add(2 * time.Second)
	// Independent store handles contend on the same wallet. No 1001st in-flight
	// broadcast fits, and releasing failed sends restores every fractional fee.
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := make([]string, 0, 1000)
	rejected := 0
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ledger := f.Reopen()
			for n := worker; n < 1008; n += 8 {
				key := fmt.Sprintf("capacity-%d", n)
				_, err := ledger.ReserveBotBroadcast(ctx, domain.BotBroadcastRequest{BotID: f.BotID, Key: key, AllowPaid: true, Now: now})
				mu.Lock()
				if err == nil {
					accepted = append(accepted, key)
				} else if errors.Is(err, domain.ErrBotBroadcastLimit) {
					rejected++
				} else {
					t.Errorf("capacity reserve=%v", err)
				}
				mu.Unlock()
			}
		}(worker)
	}
	wg.Wait()
	if len(accepted) != 1000 || rejected != 8 {
		t.Fatalf("capacity accepted=%d rejected=%d", len(accepted), rejected)
	}
	for _, key := range accepted {
		if err := f.Ledger.ReleaseBotBroadcast(ctx, domain.BotBroadcastRequest{BotID: f.BotID, Key: key, Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	balance(domain.StarsAmount{Amount: 10_999})
}
