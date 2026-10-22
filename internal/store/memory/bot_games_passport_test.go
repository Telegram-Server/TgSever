package memory

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotGamesPassportMemory(t *testing.T) {
	gp := NewGamesPassportStore()
	messages := NewMessageStore()
	messages.BindGamesPassportStore(gp)
	storetest.BotGamesPassport(t, gp, messages, 1001, 1002, 1003)
}
