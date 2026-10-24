package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestCommunityBanAdminProtectionPostgres(t *testing.T) {
	pool := testTx(t)
	ids := memberRulesUsers(t, pool, 5)
	storetest.CommunityBanAdminProtection(t, NewCommunityStore(pool, nil, nil), NewChannelStore(pool), [5]int64{ids[0], ids[1], ids[2], ids[3], ids[4]})
}
