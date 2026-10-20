package memory

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestReactionChangesMemory(t *testing.T) {
	storetest.ReactionChanges(t, storetest.ReactionChangeFixture{Messages: NewMessageStore(), Channels: NewChannelStore(), Owner: 101, Member: 102})
}

func TestBotAPIReactionUpdatesMemory(t *testing.T) {
	storetest.BotAPIReactionUpdates(t, storetest.BotAPIMemberUpdateFixture{Store: NewBotAPIUpdateStore(), BotID: 1001, OtherUserID: 2002})
}
