package memory

import (
	"context"
	"slices"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AccountPermissionsStore 是 store.AccountPermissionsStore 的内存实现（#119）。账号是否存在
// 读同一个内存 UserStore；账号注销经 WithAccountLifecyclePermissions 接入。
type AccountPermissionsStore struct {
	users *UserStore

	mu     sync.Mutex
	byUser map[int64]domain.AccountPermissions
}

var _ store.AccountPermissionsStore = (*AccountPermissionsStore)(nil)

// NewAccountPermissionsStore 创建与 users 共享账号数据的内存权限设置 store。
func NewAccountPermissionsStore(users *UserStore) *AccountPermissionsStore {
	return &AccountPermissionsStore{users: users, byUser: make(map[int64]domain.AccountPermissions)}
}

func (s *AccountPermissionsStore) GetAccountPermissions(_ context.Context, userID int64) (domain.AccountPermissions, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byUser[userID]
	if !ok {
		return domain.AccountPermissions{}, false, nil
	}
	p.SendWhitelist = slices.Clone(p.SendWhitelist)
	return p, true, nil
}

func (s *AccountPermissionsStore) SetAccountPermissions(ctx context.Context, p domain.AccountPermissions) (domain.AccountPermissions, error) {
	ids := append([]int64{p.UserID}, p.SendWhitelist...)
	for _, id := range ids {
		_, found, err := s.users.ByID(ctx, id)
		if err != nil {
			return domain.AccountPermissions{}, err
		}
		if !found {
			return domain.AccountPermissions{}, domain.ErrUserNotFound
		}
	}
	whitelist := slices.Clone(p.SendWhitelist)
	slices.Sort(whitelist)
	p.SendWhitelist = slices.Compact(whitelist)
	if p.SendWhitelist == nil {
		p.SendWhitelist = []int64{}
	}
	p.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	s.byUser[p.UserID] = p
	s.mu.Unlock()
	p.SendWhitelist = slices.Clone(p.SendWhitelist)
	return p, nil
}

// DeleteAccountPermissions 删除被注销账号的权限设置，以及别的账号白名单里指向它的条目。
func (s *AccountPermissionsStore) DeleteAccountPermissions(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byUser, userID)
	for owner, p := range s.byUser {
		if i := slices.Index(p.SendWhitelist, userID); i >= 0 {
			p.SendWhitelist = slices.Delete(slices.Clone(p.SendWhitelist), i, i+1)
			s.byUser[owner] = p
		}
	}
	return nil
}
