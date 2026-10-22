package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotInteractionsPostgres(t *testing.T) {
	storetest.BotInteractions(t, func(t *testing.T) storetest.BotInteractionsFixture {
		ctx := context.Background()
		pool := testPool(t)
		users := NewUserStore(pool)
		bots := NewBotStore(pool)
		suffix := randomSuffix(t)
		owner, err := users.Create(ctx, domain.User{Phone: "+1997" + suffix + "1", FirstName: "Owner"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := users.Create(ctx, domain.User{Phone: "+1997" + suffix + "2", FirstName: "Other"})
		if err != nil {
			t.Fatal(err)
		}
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Inline", Username: fmt.Sprintf("inter_%s_bot", suffix)}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		foreign, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Other Bot", Username: fmt.Sprintf("other_%s_bot", suffix)}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "foreign"})
		if err != nil {
			t.Fatal(err)
		}
		queries := NewBotGuestQueryStore(pool)
		return storetest.BotInteractionsFixture{Queries: queries, Feedback: queries, Buttons: bots, BotID: bot.ID, OtherBotID: foreign.ID, UserID: owner.ID, OtherUserID: other.ID}
	})
}
