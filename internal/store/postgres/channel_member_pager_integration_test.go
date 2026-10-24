package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestChannelMemberPagerContractPostgres(t *testing.T) {
	pool := testTx(t)
	channels := NewChannelStore(pool)
	storetest.ChannelMemberPagerContract(t, func(t *testing.T) storetest.ChannelMemberPagerFixture {
		ctx := context.Background()
		users := make([]int64, 0, 6)
		for _, name := range []string{"pager-a", "pager-b", "pager-c", "pager-d", "pager-e", "pager-f"} {
			users = append(users, createRevokeTestUser(t, ctx, pool, name))
		}
		return storetest.ChannelMemberPagerFixture{Channels: channels, Users: users}
	})
}
