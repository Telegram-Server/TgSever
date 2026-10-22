package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
)

func TestBotBroadcastMemory(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	bots := NewBotStore(users)
	messages := NewMessageStore()
	stars := NewStarsStore()
	owner, err := users.Create(ctx, domain.User{FirstName: "Broadcast owner", Phone: "+15550101"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{FirstName: "Broadcast recipient", Phone: "+15550102"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Broadcast bot", Username: "broadcast_contract_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: strings.Repeat("b", domain.BotTokenSecretLength)})
	if err != nil {
		t.Fatal(err)
	}
	commerce := NewBotCommerceStore(stars, bots, messages, nil)
	storetest.BotBroadcast(t, storetest.BotBroadcastFixture{Ledger: commerce, Reopen: func() store.BotBroadcastStore { return commerce }, Messages: messages, Commerce: commerce, BotID: bot.ID, OwnerID: owner.ID, RecipientID: recipient.ID, Fund: func(amount int64) error {
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
