package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestChannelRestrictionExpiryPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	storetest.ChannelRestrictionExpiry(t, NewChannelStore(pool), [4]int64{ids[0], ids[1], ids[2], ids[3]})
}
