package postgres

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelMemberTagsPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 2)
	storetest.ChannelMemberTags(t, NewChannelStore(pool), ids[0], ids[1])
}

func TestChannelMessageRanksPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 2)
	storetest.ChannelMessageRanks(t, NewChannelStore(pool), ids[0], ids[1])
}
