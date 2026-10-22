package memory_test

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestBotDefaultAdminRightsMemory(t *testing.T) {
	storetest.BotDefaultAdminRights(t, func(t *testing.T) storetest.BotDefaultAdminRightsFixture {
		ctx := context.Background()
		users := memory.NewUserStore()
		owner, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550008001", FirstName: "Owner"})
		if err != nil {
			t.Fatal(err)
		}
		bots := memory.NewBotStore(users)
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 2, FirstName: "RightsBot", Username: "rights_scope_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "s"})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.BotDefaultAdminRightsFixture{Bots: bots, Users: users, BotID: bot.ID}
	})
}
