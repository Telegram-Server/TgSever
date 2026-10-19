package memory

import (
	"fmt"
	"sync/atomic"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestLoginPasswordAccountDeletionContract(t *testing.T) {
	var seq atomic.Int64
	storetest.LoginPasswordAccountDeletionContract(t, func(t *testing.T) storetest.LoginPasswordAccountDeletionFixture {
		users := NewUserStore()
		passwords := NewLoginPasswordStore(users)
		n := seq.Add(2)
		return storetest.LoginPasswordAccountDeletionFixture{
			Users:      users,
			Passwords:  passwords,
			Lifecycle:  NewAccountLifecycleStore(users, passwords),
			Phone:      fmt.Sprintf("1555000%04d", 5100+n),
			OtherPhone: fmt.Sprintf("1555000%04d", 5101+n),
		}
	})
}
