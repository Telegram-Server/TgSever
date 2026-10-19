package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/postgres/pgtest"
)

// TestPremiumBoostListsProjectUsersForViewerPostgres 用真实 postgres 存储跑同一组断言：
// 早先 postgres 的 boost 列表把 users 表原始行（含手机号）直接带给 rpc 输出（#40）。
func TestPremiumBoostListsProjectUsersForViewerPostgres(t *testing.T) {
	pool, err := postgres.Open(context.Background(), pgtest.TestDSN(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	users := postgres.NewUserStore(pool)
	lifecycle := postgres.NewAccountLifecycleStore(pool)
	fixtures := 0
	runBoostListPrivacy(t, func(t *testing.T) boostPrivacyBackend {
		// 子测试共用本测试的克隆库，手机号按 fixture 序号区分。
		fixtures++
		fixture := fixtures
		return boostPrivacyBackend{
			users:    users,
			channels: postgres.NewChannelStore(pool),
			phone:    func(i int) string { return fmt.Sprintf("+194000%02d%02d", fixture, i) },
			createDeleted: func(t *testing.T, u domain.User) domain.User {
				t.Helper()
				created, err := users.Create(context.Background(), u)
				if err != nil {
					t.Fatalf("create user to delete: %v", err)
				}
				// 走真实注销路径：postgres 的注销行按约束清空资料，墓碑形状与线上一致。
				if _, err := lifecycle.ExecuteAccountDeletion(context.Background(), created.ID, domain.AccountDeletionManual, "test", time.Now()); err != nil {
					t.Fatalf("delete %s: %v", created.FirstName, err)
				}
				return created
			},
		}
	})
}
