package memory

import (
	"fmt"
	"sync/atomic"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestLoginPasswordContract(t *testing.T) {
	var seq atomic.Int64
	storetest.LoginPasswordContract(t, func(t *testing.T) storetest.LoginPasswordFixture {
		users := NewUserStore()
		return storetest.LoginPasswordFixture{
			Users:     users,
			Passwords: NewLoginPasswordStore(users),
			Phone:     fmt.Sprintf("1555000%04d", 4100+seq.Add(1)),
		}
	})
}
