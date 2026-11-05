package postgres

import (
	"testing"

	"telesrv/internal/store/storetest"
)

func TestGuestAccountContractPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.GuestAccountContract(t, func(t *testing.T) storetest.GuestAccountFixture {
		return storetest.GuestAccountFixture{
			Accounts: NewAccountCreationStore(pool),
			Users:    NewUserStore(pool),
			Guests:   NewGuestAccountStore(pool),
			NewPhone: func(t *testing.T) string {
				return "1996" + randomSuffix(t)
			},
		}
	})
}
