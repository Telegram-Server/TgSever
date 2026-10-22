package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestBotDefaultAdminRightsPostgres(t *testing.T) {
	pool := testPool(t)
	bots := NewBotStore(pool)
	storetest.BotDefaultAdminRights(t, func(t *testing.T) storetest.BotDefaultAdminRightsFixture {
		bot, _, _ := menuButtonFixture(t, pool)
		return storetest.BotDefaultAdminRightsFixture{Bots: bots, Users: NewUserStore(pool), BotID: bot.ID}
	})
}
