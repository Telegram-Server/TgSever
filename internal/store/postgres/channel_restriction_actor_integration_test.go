package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestChannelRestrictionActorPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	storetest.ChannelRestrictionActor(t, NewChannelStore(pool), [4]int64{ids[0], ids[1], ids[2], ids[3]})
}

func TestChannelBannedFilterEachRightPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 2)
	storetest.ChannelBannedFilterEachRight(t, NewChannelStore(pool), [2]int64{ids[0], ids[1]})
}
