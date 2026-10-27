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

func TestAccountCreationContract(t *testing.T) {
	var seq atomic.Int64
	storetest.AccountCreationContract(t, func(t *testing.T) storetest.AccountCreationFixture {
		users := NewUserStore()
		passwords := NewLoginPasswordStore(users)
		perms := NewAccountPermissionsStore(users)
		return storetest.AccountCreationFixture{
			Accounts:    NewAccountCreationStore(users, passwords, perms),
			Users:       users,
			Passwords:   passwords,
			Permissions: perms,
			NewPhone: func(*testing.T) string {
				return fmt.Sprintf("1555021%04d", seq.Add(1))
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				lifecycle := NewAccountLifecycleStore(users, passwords, WithAccountLifecyclePermissions(perms))
				if _, err := lifecycle.ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "account creation contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
