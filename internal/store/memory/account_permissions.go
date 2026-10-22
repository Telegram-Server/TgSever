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
	// writes 是每个账号的写入次数，UpdateAccountPermissions 用它确认锁外合并期间没有别的写入。
	writes map[int64]uint64
}

var _ store.AccountPermissionsStore = (*AccountPermissionsStore)(nil)

// NewAccountPermissionsStore 创建与 users 共享账号数据的内存权限设置 store。
func NewAccountPermissionsStore(users *UserStore) *AccountPermissionsStore {
	return &AccountPermissionsStore{users: users, byUser: make(map[int64]domain.AccountPermissions), writes: make(map[int64]uint64)}
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
	if err := s.checkUsers(ctx, p); err != nil {
		return domain.AccountPermissions{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(p), nil
}

func (s *AccountPermissionsStore) UpdateAccountPermissions(ctx context.Context, userID int64, merge store.AccountPermissionsMerge) (domain.AccountPermissions, bool, domain.AccountPermissions, error) {
	// 账号存在性要查 UserStore（它有自己的锁），不能在 s.mu 内查，否则与注销路径的加锁顺序相反。
	// 所以按"读快照 → 锁外合并并校验 → 锁内确认快照没变再写"做乐观并发，变了就重来；效果与
	// postgres 的行锁排队一样：后一次合并总是基于前一次写入后的值。
	for {
		s.mu.Lock()
		prev, found := s.byUser[userID]
		seen := s.writes[userID]
		s.mu.Unlock()
		prev.SendWhitelist = slices.Clone(prev.SendWhitelist)

		cur := prev
		cur.SendWhitelist = slices.Clone(prev.SendWhitelist)
		next := merge(cur, found)
		next.UserID = userID
		if err := s.checkUsers(ctx, next); err != nil {
			return domain.AccountPermissions{}, false, domain.AccountPermissions{}, err
		}

		s.mu.Lock()
		if s.writes[userID] == seen {
			written := s.writeLocked(next)
			s.mu.Unlock()
			return prev, found, written, nil
		}
		s.mu.Unlock()
	}
}

func (s *AccountPermissionsStore) checkUsers(ctx context.Context, p domain.AccountPermissions) error {
	for _, id := range append([]int64{p.UserID}, p.SendWhitelist...) {
		_, found, err := s.users.ByID(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return domain.ErrUserNotFound
		}
	}
	return nil
}

func (s *AccountPermissionsStore) writeLocked(p domain.AccountPermissions) domain.AccountPermissions {
	p.SendWhitelist = domain.NormalizeSendWhitelist(p.SendWhitelist)
	p.UpdatedAt = time.Now().UTC()
	s.byUser[p.UserID] = p
	s.writes[p.UserID]++
	p.SendWhitelist = slices.Clone(p.SendWhitelist)
	return p
}

// DeleteAccountPermissions 删除被注销账号的权限设置，以及别的账号白名单里指向它的条目。
func (s *AccountPermissionsStore) DeleteAccountPermissions(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byUser, userID)
	s.writes[userID]++
	for owner, p := range s.byUser {
		if i := slices.Index(p.SendWhitelist, userID); i >= 0 {
			p.SendWhitelist = slices.Delete(slices.Clone(p.SendWhitelist), i, i+1)
			s.byUser[owner] = p
			s.writes[owner]++
		}
	}
	return nil
}
