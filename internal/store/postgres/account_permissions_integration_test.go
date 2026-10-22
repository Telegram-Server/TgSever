package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestAccountPermissionsContractPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	perms := NewAccountPermissionsStore(pool)
	storetest.AccountPermissionsContract(t, func(t *testing.T) storetest.AccountPermissionsFixture {
		return storetest.AccountPermissionsFixture{
			Users:       users,
			Permissions: perms,
			NewPhone: func(t *testing.T) string {
				return "1996" + randomSuffix(t)
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				if _, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "permissions contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
