package memory

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestOnboardingContract(t *testing.T) {
	var seq atomic.Int64
	storetest.OnboardingContract(t, func(t *testing.T) storetest.OnboardingFixture {
		users := NewUserStore()
		onboarding := NewOnboardingStore()
		newPhone := func(*testing.T) string { return fmt.Sprintf("1555031%04d", seq.Add(1)) }
		return storetest.OnboardingFixture{
			Accounts:   NewAccountCreationStore(users, nil, nil).WithOnboarding(onboarding),
			Onboarding: onboarding,
			NewPhone:   newPhone,
			NewUser: func(t *testing.T) domain.User {
				u, err := users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Lead", Phone: newPhone(t)})
				if err != nil {
					t.Fatalf("create user: %v", err)
				}
				return u
			},
			// 内存模板不检查群是否存在，给一个不重复的 id 即可。
			NewChannel: func(*testing.T, int64) int64 { return 1<<40 + seq.Add(1) },
		}
	})
}
