package postgres

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotDefaultAdminRightsPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.BotDefaultAdminRights(t, func(t *testing.T) storetest.BotDefaultAdminRightsFixture {
		bot, _, _ := menuButtonFixture(t, pool)
		return storetest.BotDefaultAdminRightsFixture{Bots: NewBotStore(pool), BotID: bot.ID}
	})
}
