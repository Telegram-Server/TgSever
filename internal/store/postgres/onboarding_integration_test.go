package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/pgtest"
	"telesrv/internal/store/storetest"
)

func TestOnboardingContractPostgres(t *testing.T) {
	storetest.OnboardingContract(t, func(t *testing.T) storetest.OnboardingFixture {
		pool, err := Open(context.Background(), pgtest.NewDatabase(t))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(pool.Close)
		users := NewUserStore(pool)
		newPhone := func(t *testing.T) string { return "1993" + randomSuffix(t) }
		return storetest.OnboardingFixture{
			Accounts:   NewAccountCreationStore(pool),
			Onboarding: NewOnboardingStore(pool),
			NewPhone:   newPhone,
			NewUser: func(t *testing.T) domain.User {
				u, err := users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Lead", Phone: newPhone(t)})
				if err != nil {
					t.Fatalf("create user: %v", err)
				}
				return u
			},
			NewChannel: func(t *testing.T, ownerUserID int64) int64 {
				res, err := NewChannelStore(pool).CreateChannel(context.Background(), domain.CreateChannelRequest{
					CreatorUserID: ownerUserID, Title: "Onboarding", Megagroup: true, Date: int(time.Now().Unix()),
				})
				if err != nil {
					t.Fatalf("create channel: %v", err)
				}
				return res.Channel.ID
			},
		}
	})
}
