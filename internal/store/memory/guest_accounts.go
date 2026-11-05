package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// GuestAccountStore 是 store.GuestAccountStore 的内存实现。写入只经
// AccountCreationStore.CreateAccount（WithGuests 接上之后），在持有 users.mu 时进行。
type GuestAccountStore struct {
	mu       sync.Mutex
	accounts map[int64]domain.GuestAccount
	bindings map[string]domain.GuestIPBinding
	now      func() time.Time
}

var _ store.GuestAccountStore = (*GuestAccountStore)(nil)

func NewGuestAccountStore() *GuestAccountStore {
	return &GuestAccountStore{
		accounts: make(map[int64]domain.GuestAccount),
		bindings: make(map[string]domain.GuestIPBinding),
		now:      time.Now,
	}
}

// SetClock 替换写入 created_at / bound_at 用的时钟（测试用）。
func (s *GuestAccountStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

func (s *GuestAccountStore) GuestIPBinding(_ context.Context, ipKey string) (domain.GuestIPBinding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[ipKey]
	return b, ok, nil
}

func (s *GuestAccountStore) GuestAccount(_ context.Context, userID int64) (domain.GuestAccount, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[userID]
	return a, ok, nil
}

// checkBindingLocked 是 BindGuestIP 的比较并交换判断：没有绑定时只接受 previous 为 nil，
// 有绑定时只接受 previous 等于当前绑定的账号。
func (s *GuestAccountStore) checkBindingLocked(guest store.GuestAccountExtras) error {
	current, ok := s.bindings[guest.IPKey]
	if !ok {
		return nil
	}
	if guest.PreviousUserID == nil || *guest.PreviousUserID != current.UserID {
		return domain.ErrGuestIPBindingChanged
	}
	return nil
}

func (s *GuestAccountStore) writeLocked(created domain.User, guest store.GuestAccountExtras) {
	now := s.now().UTC()
	s.accounts[created.ID] = domain.GuestAccount{UserID: created.ID, Phone: created.Phone, IPKey: guest.IPKey, CreatedAt: now}
	s.bindings[guest.IPKey] = domain.GuestIPBinding{IPKey: guest.IPKey, UserID: created.ID, BoundAt: now}
}
