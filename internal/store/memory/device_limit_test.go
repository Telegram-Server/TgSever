package memory

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestDeviceLimitContract(t *testing.T) {
	var seq atomic.Int64
	storetest.DeviceLimitContract(t, func(t *testing.T) storetest.DeviceLimitFixture {
		users := NewUserStore()
		keys := NewAuthKeyStore()
		auths := NewAuthorizationStore()
		auths.LinkAuthKeyAuthority(keys)
		auths.LinkAuthorizationUsers(users)
		return storetest.DeviceLimitFixture{
			Users:        users,
			AuthKeys:     keys,
			Auths:        auths,
			DeviceLimits: auths,
			NewPhone: func(*testing.T) string {
				return fmt.Sprintf("1555010%04d", seq.Add(1))
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				lifecycle := NewAccountLifecycleStore(users, nil, WithAccountLifecycleAuthorizations(auths))
				if _, err := lifecycle.ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "device binding contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
			NewBot: func(t *testing.T) domain.User {
				bot, err := users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Bot", Bot: true})
				if err != nil {
					t.Fatalf("create bot: %v", err)
				}
				return bot
			},
		}
	})
}
