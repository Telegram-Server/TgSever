package postgres

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelJoinEventSnapshotsPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	storetest.ChannelJoinEventSnapshots(t, NewChannelStore(pool), [4]int64{ids[0], ids[1], ids[2], ids[3]})
}
