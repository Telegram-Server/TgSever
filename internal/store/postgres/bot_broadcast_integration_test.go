package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
)

func TestBotBroadcastPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	bots := NewBotStore(pool)
	messages := NewMessageStore(pool)
	stars := NewStarsStore(pool)
	owner := createTestUser(t, ctx, users, "+1898"+suffix+"01", "Broadcast owner", "")
	recipient := createTestUser(t, ctx, users, "+1898"+suffix+"02", "Broadcast recipient", "")
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 9981, FirstName: "Broadcast bot", Username: "broadcast_" + suffix + "_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: strings.Repeat("b", domain.BotTokenSecretLength)})
	if err != nil {
		t.Fatal(err)
	}
	commerce := NewBotCommerceStore(pool)
	storetest.BotBroadcast(t, storetest.BotBroadcastFixture{Ledger: commerce, Reopen: func() store.BotBroadcastStore { return NewBotCommerceStore(pool) }, Messages: messages, Commerce: commerce, BotID: bot.ID, OwnerID: owner.ID, RecipientID: recipient.ID, Fund: func(amount int64) error {
		date := int(time.Now().Unix())
		if _, err := stars.Credit(ctx, owner.ID, amount, domain.StarsReasonTopup, domain.Peer{}, date, "fund broadcasting", ""); err != nil {
			return err
		}
		for amount > 0 {
			part := min(amount, 10_000)
			if err := commerce.BusinessTransfer(ctx, owner.ID, bot.ID, part, date); err != nil {
				return err
			}
			amount -= part
		}
		return nil
	}})
}
