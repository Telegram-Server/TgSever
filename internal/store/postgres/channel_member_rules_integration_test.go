package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// memberRulesUsers 建 n 个测试用户，结束时连同其建的频道一起清理。
func memberRulesUsers(t *testing.T, pool *pgxpool.Pool, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(990 + i), Phone: fmt.Sprintf("+178%s%02d", suffix, i), FirstName: "Rules"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE creator_user_id=ANY($1::bigint[])`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, ids)
	})
	return ids
}

func TestChannelMemberPreviousStatesPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 5)
	storetest.ChannelMemberPreviousStates(t, NewChannelStore(pool), [5]int64{ids[0], ids[1], ids[2], ids[3], ids[4]})
}

func TestChannelOwnerLeavePreviousPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 3)
	storetest.ChannelOwnerLeavePrevious(t, NewChannelStore(pool), [3]int64{ids[0], ids[1], ids[2]})
}

func TestChannelAdminProtectionPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	storetest.ChannelAdminProtection(t, NewChannelStore(pool), [4]int64{ids[0], ids[1], ids[2], ids[3]})
}

func TestChannelHideAllJoinRequestsExcludePostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 3)
	storetest.ChannelHideAllJoinRequestsExclude(t, NewChannelStore(pool), [3]int64{ids[0], ids[1], ids[2]})
}
