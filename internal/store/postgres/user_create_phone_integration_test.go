package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestUserCreatePhoneConflictsPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	storetest.UserCreatePhoneConflicts(t, func(t *testing.T) storetest.UserCreatePhoneFixture {
		phone := "1999" + randomSuffix(t)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE phone = $1", phone)
		})
		return storetest.UserCreatePhoneFixture{Store: users, Phone: phone}
	})
}
