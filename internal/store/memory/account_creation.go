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
// 加锁顺序固定为 users.mu → passwords.mu → permissions.mu，与注销（users.mu → loginPasswords.mu，
// 放开后再拿 permissions.mu）和各 store 自己的路径都不反向嵌套。所有校验在改动任何状态之前
// 完成，之后的写入本身不会失败，所以不会留下只建了一半的账号。
type AccountCreationStore struct {
	users       *UserStore
	passwords   *LoginPasswordStore
	permissions *AccountPermissionsStore
}

var _ store.AccountCreationStore = (*AccountCreationStore)(nil)

// NewAccountCreationStore 创建与 users 共享账号数据的内存建号操作。
func NewAccountCreationStore(users *UserStore, passwords *LoginPasswordStore, permissions *AccountPermissionsStore) *AccountCreationStore {
	return &AccountCreationStore{users: users, passwords: passwords, permissions: permissions}
}

func (s *AccountCreationStore) CreateAccount(_ context.Context, u domain.User, extras store.AccountExtras) (domain.User, error) {
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

	created, err := s.users.createLocked(u)
	if err != nil {
		return domain.User{}, err
	}
	if extras.LoginPassword != nil {
		s.passwords.putLocked(created.ID, *extras.LoginPassword, time.Now().UTC())
	}
	if extras.Permissions != nil {
		perms.UserID = created.ID
		s.permissions.writeLocked(perms)
	}
	return created, nil
}
