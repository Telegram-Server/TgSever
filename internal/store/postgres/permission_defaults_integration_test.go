package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/pgtest"
	"telesrv/internal/store/storetest"
)

// 模板是全库唯一的一行，每个子测试用自己的数据库，才能从"没有模板"开始。
func TestPermissionDefaultsContractPostgres(t *testing.T) {
	storetest.PermissionDefaultsContract(t, func(t *testing.T) storetest.PermissionDefaultsFixture {
		pool, err := Open(context.Background(), pgtest.NewDatabase(t))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(pool.Close)
		return storetest.PermissionDefaultsFixture{
			Users:    NewUserStore(pool),
			Defaults: NewAccountPermissionsStore(pool),
			NewPhone: func(t *testing.T) string {
				return "1997" + randomSuffix(t)
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				if _, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "defaults contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
