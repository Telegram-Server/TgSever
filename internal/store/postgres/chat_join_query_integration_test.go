package postgres

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChatJoinQueriesPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	s := NewChannelStore(pool)
	storetest.ChatJoinQueries(t, s, func() store.ChannelStore { return NewChannelStore(pool) }, ids[0], ids[1], ids[2], ids[3])
}
