package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestLoginPasswordContractPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	passwords := NewLoginPasswordStore(pool)
	storetest.LoginPasswordContract(t, func(t *testing.T) storetest.LoginPasswordFixture {
		phone := "1998" + randomSuffix(t)
		return storetest.LoginPasswordFixture{Users: users, Passwords: passwords, Phone: phone}
	})
}
