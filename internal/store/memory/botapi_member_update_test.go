package memory

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestBotAPIMemberUpdates(t *testing.T) {
	storetest.BotAPIMemberUpdates(t, func(t *testing.T) storetest.BotAPIMemberUpdateFixture {
		return storetest.BotAPIMemberUpdateFixture{Store: NewBotAPIUpdateStore(), BotID: 1001, OtherUserID: 2002}
	})
}

func TestBotAPIJoinRequestUpdates(t *testing.T) {
	storetest.BotAPIJoinRequestUpdates(t, func(t *testing.T) storetest.BotAPIMemberUpdateFixture {
		return storetest.BotAPIMemberUpdateFixture{Store: NewBotAPIUpdateStore(), BotID: 1001, OtherUserID: 2002}
	})
}
