package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/store"
)

// loginPasswordChallengeSweepInterval 是清理过期 challenge 的最小间隔。
const loginPasswordChallengeSweepInterval = time.Minute

// LoginPasswordChallengeStore 是 store.LoginPasswordChallengeStore 的内存实现。
// 与 passkey 挑战、QR 登录 token 一样是进程内一次性凭据，单体部署不跨实例。
type LoginPasswordChallengeStore struct {
	now func() time.Time

	mu        sync.Mutex
	m         map[int64]store.LoginPasswordChallenge
	lastSweep time.Time
}

// NewLoginPasswordChallengeStore 创建内存 challenge store。now 只用于清理过期记录
// （过期判断本身由调用方按 ExpiresAt 做）；为 nil 时用 time.Now。
func NewLoginPasswordChallengeStore(now func() time.Time) *LoginPasswordChallengeStore {
	if now == nil {
		now = time.Now
	}
	return &LoginPasswordChallengeStore{now: now, m: make(map[int64]store.LoginPasswordChallenge)}
}

func (s *LoginPasswordChallengeStore) SaveLoginPasswordChallenge(_ context.Context, c store.LoginPasswordChallenge) error {
	if c.SRPID == 0 {
		return store.ErrLoginPasswordChallengeExists
	}
	c = cloneLoginPasswordChallenge(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if _, ok := s.m[c.SRPID]; ok {
		return store.ErrLoginPasswordChallengeExists
	}
	s.m[c.SRPID] = c
	return nil
}

func (s *LoginPasswordChallengeStore) TakeLoginPasswordChallenge(_ context.Context, srpID int64) (store.LoginPasswordChallenge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[srpID]
	if !ok {
		return store.LoginPasswordChallenge{}, false, nil
	}
	delete(s.m, srpID) // 一次性：无论之后校验成败都作废
	return c, true, nil
}

// sweepLocked 每隔 loginPasswordChallengeSweepInterval 删除一次已过期的记录，避免没人
// 来取的 challenge 一直占着内存。调用方必须持有 s.mu。
func (s *LoginPasswordChallengeStore) sweepLocked() {
	now := s.now()
	if now.Sub(s.lastSweep) < loginPasswordChallengeSweepInterval {
		return
	}
	s.lastSweep = now
	for id, c := range s.m {
		if !now.Before(c.ExpiresAt) {
			delete(s.m, id)
		}
	}
}

func cloneLoginPasswordChallenge(c store.LoginPasswordChallenge) store.LoginPasswordChallenge {
	c.Secret = append([]byte(nil), c.Secret...)
	c.B = append([]byte(nil), c.B...)
	c.Verifier = append([]byte(nil), c.Verifier...)
	c.Salt1 = append([]byte(nil), c.Salt1...)
	c.Salt2 = append([]byte(nil), c.Salt2...)
	return c
}
