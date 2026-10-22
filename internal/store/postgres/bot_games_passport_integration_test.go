package postgres

import (
	"context"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotGamesPassportPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1899"+suffix+"01", "Passport owner", "")
	bots := NewBotStore(pool)
	createBot := func(name string) domain.User {
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 9987, FirstName: name, Username: name + "_" + suffix + "_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: strings.Repeat("p", domain.BotTokenSecretLength)})
		if err != nil {
			t.Fatal(err)
		}
		return bot
	}
	bot, other := createBot("passport"), createBot("otherpassport")
	storetest.BotGamesPassport(t, NewGamesPassportStore(pool), NewMessageStore(pool), owner.ID, bot.ID, other.ID)
}
