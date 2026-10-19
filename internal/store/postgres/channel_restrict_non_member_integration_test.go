package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestChannelRestrictNonMemberPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 5)
	storetest.ChannelRestrictNonMember(t, NewChannelStore(pool), [5]int64{ids[0], ids[1], ids[2], ids[3], ids[4]})
}
