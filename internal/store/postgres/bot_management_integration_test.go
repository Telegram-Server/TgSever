package postgres

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotManagementPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.BotManagement(t, func(t *testing.T) storetest.BotManagementFixture {
		bot, _, _ := menuButtonFixture(t, pool)
		bots := NewBotStore(pool)
		return storetest.BotManagementFixture{Bots: bots, Management: bots, BotID: bot.ID}
	})
}
