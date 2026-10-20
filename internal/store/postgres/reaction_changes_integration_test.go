package postgres

import (
	"context"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestReactionChangesPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner := createTestUser(t, context.Background(), users, "+1998"+suffix+"01", "Owner", "")
	member := createTestUser(t, context.Background(), users, "+1998"+suffix+"02", "Member", "")
	storetest.ReactionChanges(t, storetest.ReactionChangeFixture{Messages: NewMessageStore(pool), Channels: NewChannelStore(pool), Owner: owner.ID, Member: member.ID})
}

func TestBotAPIReactionUpdatesPostgres(t *testing.T) {
	storetest.BotAPIReactionUpdates(t, botAPIMemberUpdateFixture(t, testPool(t)))
}
