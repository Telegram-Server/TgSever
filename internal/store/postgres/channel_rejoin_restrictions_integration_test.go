package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestChannelRejoinKeepsRestrictionsPostgres(t *testing.T) {
	pool := testTx(t)
	ids := memberRulesUsers(t, pool, 4)
	storetest.ChannelRejoinKeepsRestrictions(t, NewChannelStore(pool), [4]int64{ids[0], ids[1], ids[2], ids[3]})
}
