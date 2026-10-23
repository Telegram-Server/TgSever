package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestUserCreatePhoneConflictsPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	storetest.UserCreatePhoneConflicts(t, func(t *testing.T) storetest.UserCreatePhoneFixture {
		phone := "1999" + randomSuffix(t)
		return storetest.UserCreatePhoneFixture{Store: users, Phone: phone}
	})
}
