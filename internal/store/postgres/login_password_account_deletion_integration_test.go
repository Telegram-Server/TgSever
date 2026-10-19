package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

// TestLoginPasswordAccountDeletionPostgres 证明 ExecuteAccountDeletion 在注销事务里删掉了
// login_passwords 的那一行（用户是软删除，账号行还在，外键级联不会发生）。
func TestLoginPasswordAccountDeletionPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	passwords := NewLoginPasswordStore(pool)
	lifecycle := NewAccountLifecycleStore(pool)
	storetest.LoginPasswordAccountDeletionContract(t, func(t *testing.T) storetest.LoginPasswordAccountDeletionFixture {
		return storetest.LoginPasswordAccountDeletionFixture{
			Users:      users,
			Passwords:  passwords,
			Lifecycle:  lifecycle,
			Phone:      "1997" + randomSuffix(t),
			OtherPhone: "1996" + randomSuffix(t),
		}
	})
}
