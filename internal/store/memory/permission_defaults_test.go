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

func TestPermissionDefaultsContract(t *testing.T) {
	var seq atomic.Int64
	storetest.PermissionDefaultsContract(t, func(t *testing.T) storetest.PermissionDefaultsFixture {
		users := NewUserStore()
		perms := NewAccountPermissionsStore(users)
		return storetest.PermissionDefaultsFixture{
			Users:    users,
			Defaults: perms,
			NewPhone: func(*testing.T) string {
				return fmt.Sprintf("1555021%04d", seq.Add(1))
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				lifecycle := NewAccountLifecycleStore(users, nil, WithAccountLifecyclePermissions(perms))
				if _, err := lifecycle.ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "defaults contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
