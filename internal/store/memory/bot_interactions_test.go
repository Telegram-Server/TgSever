package memory_test

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotInteractionsMemory(t *testing.T) {
	storetest.BotInteractions(t, func(t *testing.T) storetest.BotInteractionsFixture {
		ctx := context.Background()
		users := memory.NewUserStore()
		bots := memory.NewBotStore(users)
		owner, err := users.Create(ctx, domain.User{Phone: "15550009991", FirstName: "Owner"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := users.Create(ctx, domain.User{Phone: "15550009992", FirstName: "Other"})
		if err != nil {
			t.Fatal(err)
		}
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Inline", Username: "interaction_test_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		foreign, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Other Bot", Username: "interaction_other_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "foreign"})
		if err != nil {
			t.Fatal(err)
		}
		queries := memory.NewBotGuestQueryStore()
		return storetest.BotInteractionsFixture{Queries: queries, Feedback: queries, Buttons: bots, BotID: bot.ID, OtherBotID: foreign.ID, UserID: owner.ID, OtherUserID: other.ID}
	})
}
