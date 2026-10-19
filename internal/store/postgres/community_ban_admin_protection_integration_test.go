package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestCommunityBanAdminProtectionPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 5)
	// memberRulesUsers 的清理会删用户，社区引用用户，必须先删（t.Cleanup 后注册先执行）。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM communities WHERE creator_user_id=$1`, ids[0])
	})
	storetest.CommunityBanAdminProtection(t, NewCommunityStore(pool, nil, nil), NewChannelStore(pool), [5]int64{ids[0], ids[1], ids[2], ids[3], ids[4]})
}
