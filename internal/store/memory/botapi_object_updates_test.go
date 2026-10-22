package memory

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotAPIObjectUpdates(t *testing.T) {
	storetest.BotAPIObjectUpdates(t, storetest.BotAPIMemberUpdateFixture{Store: NewBotAPIUpdateStore(), BotID: 1001, OtherUserID: 2002})
}
