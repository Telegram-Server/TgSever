package memory

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AccountCreationStore 是 store.AccountCreationStore 的内存实现（#159）。passwords 与
// permissions 必须和 users 共享同一份账号数据，可以为 nil（此时对应的附加项被拒绝）。
//
// 加锁顺序固定为 users.mu → passwords.mu → permissions.mu（→ guests.mu → onboarding.mu），与注销（users.mu → loginPasswords.mu，
// 放开后再拿 permissions.mu）和各 store 自己的路径都不反向嵌套。所有校验在改动任何状态之前
// 完成，之后的写入本身不会失败，所以不会留下只建了一半的账号。
type AccountCreationStore struct {
	users       *UserStore
	passwords   *LoginPasswordStore
	permissions *AccountPermissionsStore
	guests      *GuestAccountStore
	onboarding  *OnboardingStore
}

var _ store.AccountCreationStore = (*AccountCreationStore)(nil)

// NewAccountCreationStore 创建与 users 共享账号数据的内存建号操作。
func NewAccountCreationStore(users *UserStore, passwords *LoginPasswordStore, permissions *AccountPermissionsStore) *AccountCreationStore {
	return &AccountCreationStore{users: users, passwords: passwords, permissions: permissions}
}

// WithGuests 接上游客标记与 IP 绑定的存储，之后才接受 AccountExtras.Guest。guests.mu 排在
// permissions.mu 之后。
func (s *AccountCreationStore) WithGuests(guests *GuestAccountStore) *AccountCreationStore {
	s.guests = guests
	return s
}

// WithOnboarding 接上入驻 store，之后建号时按当时的模板写入入驻任务。onboarding.mu 排在
// guests.mu 之后。
func (s *AccountCreationStore) WithOnboarding(onboarding *OnboardingStore) *AccountCreationStore {
	s.onboarding = onboarding
	return s
}

func (s *AccountCreationStore) CreateAccount(_ context.Context, u domain.User, extras store.AccountExtras) (domain.User, error) {
	if extras.Guest != nil && (s.guests == nil || extras.Guest.IPKey == "") {
		return domain.User{}, fmt.Errorf("create account: guest store is not configured or ip key is empty")
	}
	if extras.LoginPassword != nil {
		if s.passwords == nil {
			return domain.User{}, fmt.Errorf("create account: login password store is not configured")
		}
		if !loginPasswordComplete(*extras.LoginPassword) {
			return domain.User{}, store.ErrLoginPasswordInvalid
		}
	}
	if extras.Permissions != nil && s.permissions == nil {
		return domain.User{}, fmt.Errorf("create account: permissions store is not configured")
	}

	s.users.mu.Lock()
	defer s.users.mu.Unlock()
	var perms domain.AccountPermissions
	if extras.Permissions != nil {
		// 持有 users.mu 期间校验白名单：注销要拿同一把锁才能写墓碑，校验结果在写入前不会过期。
		perms = *extras.Permissions
		perms.SendWhitelist = make([]int64, 0, len(extras.Permissions.SendWhitelist))
		for _, id := range extras.Permissions.SendWhitelist {
			member, ok := s.users.byID[id]
			if !ok {
				return domain.User{}, domain.ErrUserNotFound
			}
			if !member.Deleted {
				perms.SendWhitelist = append(perms.SendWhitelist, id)
			}
		}
	}
	if s.passwords != nil {
		s.passwords.mu.Lock()
		defer s.passwords.mu.Unlock()
	}
	if s.permissions != nil {
		s.permissions.mu.Lock()
		defer s.permissions.mu.Unlock()
	}
	if extras.Guest != nil {
		s.guests.mu.Lock()
		defer s.guests.mu.Unlock()
		if err := s.guests.checkBindingLocked(*extras.Guest); err != nil {
			return domain.User{}, err
		}
	}
	if s.onboarding != nil {
		s.onboarding.mu.Lock()
		defer s.onboarding.mu.Unlock()
	}

	created, err := s.users.createLocked(u)
	if err != nil {
		return domain.User{}, err
	}
	if extras.Guest != nil {
		s.guests.writeLocked(created, *extras.Guest)
	}
	if extras.LoginPassword != nil {
		s.passwords.putLocked(created.ID, *extras.LoginPassword, time.Now().UTC())
	}
	if extras.Permissions != nil {
		perms.UserID = created.ID
		s.permissions.writeLocked(perms)
	}
	if s.onboarding != nil {
		s.onboarding.enqueueLocked(created.ID, time.Now().UTC())
	}
	return created, nil
}
