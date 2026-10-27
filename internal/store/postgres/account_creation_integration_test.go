package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestAccountCreationContractPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.AccountCreationContract(t, func(t *testing.T) storetest.AccountCreationFixture {
		return storetest.AccountCreationFixture{
			Accounts:    NewAccountCreationStore(pool),
			Users:       NewUserStore(pool),
			Passwords:   NewLoginPasswordStore(pool),
			Permissions: NewAccountPermissionsStore(pool),
			NewPhone: func(t *testing.T) string {
				return "1995" + randomSuffix(t)
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				if _, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "account creation contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
