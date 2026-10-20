package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestLoginPasswordContractPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	passwords := NewLoginPasswordStore(pool)
	storetest.LoginPasswordContract(t, func(t *testing.T) storetest.LoginPasswordFixture {
		phone := "1998" + randomSuffix(t)
		t.Cleanup(func() {
			// login_passwords 随 users 行级联删除。
			_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE phone = $1", phone)
		})
		return storetest.LoginPasswordFixture{Users: users, Passwords: passwords, Phone: phone}
	})
}
