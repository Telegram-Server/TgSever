package memory

import (
	"fmt"
	"sync/atomic"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestGuestAccountContract(t *testing.T) {
	var seq atomic.Int64
	storetest.GuestAccountContract(t, func(t *testing.T) storetest.GuestAccountFixture {
		users := NewUserStore()
		guests := NewGuestAccountStore()
		return storetest.GuestAccountFixture{
			Accounts: NewAccountCreationStore(users, nil, nil).WithGuests(guests),
			Users:    users,
			Guests:   guests,
			NewPhone: func(*testing.T) string {
				return fmt.Sprintf("1555022%04d", seq.Add(1))
			},
		}
	})
}
