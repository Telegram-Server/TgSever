package memory_test

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotManagementMemory(t *testing.T) {
	storetest.BotManagement(t, func(t *testing.T) storetest.BotManagementFixture {
		ctx := context.Background()
		users := memory.NewUserStore()
		owner, err := users.Create(ctx, domain.User{Phone: "15550005991", FirstName: "Owner"})
		if err != nil {
			t.Fatal(err)
		}
		bots := memory.NewBotStore(users)
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Managed", Username: "managed_contract_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.BotManagementFixture{Bots: bots, Management: bots, BotID: bot.ID}
	})
}
