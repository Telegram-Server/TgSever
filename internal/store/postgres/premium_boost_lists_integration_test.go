package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestPremiumBoostListsPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 3)
	storetest.PremiumBoostLists(t, NewChannelStore(pool), [3]int64{ids[0], ids[1], ids[2]})
}
