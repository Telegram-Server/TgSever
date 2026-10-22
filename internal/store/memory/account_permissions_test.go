package memory

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestAccountPermissionsContract(t *testing.T) {
	var seq atomic.Int64
	storetest.AccountPermissionsContract(t, func(t *testing.T) storetest.AccountPermissionsFixture {
		users := NewUserStore()
		perms := NewAccountPermissionsStore(users)
		return storetest.AccountPermissionsFixture{
			Users:       users,
			Permissions: perms,
			NewPhone: func(*testing.T) string {
				return fmt.Sprintf("1555020%04d", seq.Add(1))
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				lifecycle := NewAccountLifecycleStore(users, nil, WithAccountLifecyclePermissions(perms))
				if _, err := lifecycle.ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "permissions contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
