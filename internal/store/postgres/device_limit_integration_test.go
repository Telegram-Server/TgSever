package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestDeviceLimitContractPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	auths := NewAuthorizationStore(pool)
	storetest.DeviceLimitContract(t, func(t *testing.T) storetest.DeviceLimitFixture {
		return storetest.DeviceLimitFixture{
			Users:        users,
			AuthKeys:     NewAuthKeyStore(pool),
			Auths:        auths,
			DeviceLimits: auths,
			NewPhone: func(t *testing.T) string {
				return "1997" + randomSuffix(t)
			},
			NewBot: func(t *testing.T) domain.User {
				ctx := context.Background()
				u, err := users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Bot"})
				if err != nil {
					t.Fatalf("create bot user: %v", err)
				}
				if _, err := pool.Exec(ctx, `UPDATE users SET is_bot = true WHERE id = $1`, u.ID); err != nil {
					t.Fatalf("mark bot: %v", err)
				}
				u.Bot = true
				return u
			},
		}
	})
}
