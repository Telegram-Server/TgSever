package memory_test

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotDefaultAdminRightsMemory(t *testing.T) {
	storetest.BotDefaultAdminRights(t, func(t *testing.T) storetest.BotDefaultAdminRightsFixture {
		ctx := context.Background()
		users := memory.NewUserStore()
		owner, err := users.Create(ctx, domain.User{AccessHash: 5901, Phone: "15550005901", FirstName: "Owner"})
		if err != nil {
			t.Fatal(err)
		}
		bots := memory.NewBotStore(users)
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 5902, FirstName: "Rights", Username: "rights_contract_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.BotDefaultAdminRightsFixture{Bots: bots, BotID: bot.ID}
	})
}
