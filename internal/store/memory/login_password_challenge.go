package memory

import (
	"container/list"
	"context"
	"sync"
	"time"

	"telesrv/internal/store"
)

// loginPasswordChallengeSweepInterval 是清理过期 challenge 的最小间隔。
const loginPasswordChallengeSweepInterval = time.Minute

// 挂起中的密码登录 challenge 的默认上限。满了就淘汰最旧的，不拒绝新请求：
// 满了就拒绝的话，攻击者把表填满后所有人都登录不了；淘汰的话，攻击者想挤掉正常用户，
// 必须在用户 get 与 signIn 之间（通常一秒左右）刷出约一整张表的新 challenge，
// 每一个都要服务端做一次 2048 位模幂，还要先过验证码签发的限流。
const (
	// DefaultLoginPasswordChallengesPerAuthKey：正常客户端同一时刻只有一次登录在进行，
	// 留几个余量给重试和重复点击；同一把 key 再多要的只会挤掉自己更早的那个。
	DefaultLoginPasswordChallengesPerAuthKey = 5
	// DefaultLoginPasswordChallengesTotal：每条记录约 1–1.5 KB（b、B、verifier 各 256 字节，
	// 加盐、号码与 map/list 开销），10000 条约 10–15 MB。
	DefaultLoginPasswordChallengesTotal = 10000
)

// LoginPasswordChallengeStore 是 store.LoginPasswordChallengeStore 的内存实现。
// 与 passkey 挑战、QR 登录 token 一样是进程内一次性凭据，单体部署不跨实例。
// 挂起的 challenge 每个 auth_key 和全局各有一个上限，满了淘汰最旧的。
type LoginPasswordChallengeStore struct {
	now        func() time.Time
	perAuthKey int
	total      int

	mu        sync.Mutex
	m         map[int64]*loginPasswordChallengeEntry
	order     *list.List          // 元素是 srp_id，按保存先后排列，Front 最旧
	byAuthKey map[[8]byte][]int64 // 每把 auth_key 挂起的 srp_id，按保存先后排列
	lastSweep time.Time
}

type loginPasswordChallengeEntry struct {
	c    store.LoginPasswordChallenge
	elem *list.Element
}

// LoginPasswordChallengeOption 调整内存 challenge store。
type LoginPasswordChallengeOption func(*LoginPasswordChallengeStore)

// WithLoginPasswordChallengeLimits 设置挂起 challenge 的上限：每个 auth_key 最多 perAuthKey 个，
// 全局最多 total 个，超出时淘汰最旧的。<=0 的值沿用默认上限。
func WithLoginPasswordChallengeLimits(perAuthKey, total int) LoginPasswordChallengeOption {
	return func(s *LoginPasswordChallengeStore) {
		if perAuthKey > 0 {
			s.perAuthKey = perAuthKey
		}
		if total > 0 {
			s.total = total
		}
	}
}

// NewLoginPasswordChallengeStore 创建内存 challenge store。now 只用于清理过期记录
// （过期判断本身由调用方按 ExpiresAt 做）；为 nil 时用 time.Now。
func NewLoginPasswordChallengeStore(now func() time.Time, opts ...LoginPasswordChallengeOption) *LoginPasswordChallengeStore {
	if now == nil {
		now = time.Now
	}
	s := &LoginPasswordChallengeStore{
		now:        now,
		perAuthKey: DefaultLoginPasswordChallengesPerAuthKey,
		total:      DefaultLoginPasswordChallengesTotal,
		m:          make(map[int64]*loginPasswordChallengeEntry),
		order:      list.New(),
		byAuthKey:  make(map[[8]byte][]int64),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
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
	// 先腾出这把 auth_key 的位置（只挤掉自己的），再腾全局的位置。
	for pending := s.byAuthKey[c.AuthKeyID]; len(pending) >= s.perAuthKey; pending = s.byAuthKey[c.AuthKeyID] {
		s.removeLocked(pending[0])
	}
	for len(s.m) >= s.total {
		s.removeLocked(s.order.Front().Value.(int64))
	}
	s.m[c.SRPID] = &loginPasswordChallengeEntry{c: c, elem: s.order.PushBack(c.SRPID)}
	s.byAuthKey[c.AuthKeyID] = append(s.byAuthKey[c.AuthKeyID], c.SRPID)
	return nil
}

func (s *LoginPasswordChallengeStore) TakeLoginPasswordChallenge(_ context.Context, srpID int64) (store.LoginPasswordChallenge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[srpID]
	if !ok {
		return store.LoginPasswordChallenge{}, false, nil
	}
	s.removeLocked(srpID) // 一次性：无论之后校验成败都作废
	return e.c, true, nil
}

// removeLocked 删除 srpID 对应的记录及其索引。调用方必须持有 s.mu。
func (s *LoginPasswordChallengeStore) removeLocked(srpID int64) {
	e, ok := s.m[srpID]
	if !ok {
		return
	}
	delete(s.m, srpID)
	s.order.Remove(e.elem)
	key := e.c.AuthKeyID
	pending := s.byAuthKey[key]
	for i, id := range pending {
		if id == srpID {
			pending = append(pending[:i], pending[i+1:]...)
			break
		}
	}
	if len(pending) == 0 {
		delete(s.byAuthKey, key)
	} else {
		s.byAuthKey[key] = pending
	}
}

// sweepLocked 每隔 loginPasswordChallengeSweepInterval 删除一次已过期的记录，避免没人
// 来取的 challenge 一直占着内存。调用方必须持有 s.mu。
func (s *LoginPasswordChallengeStore) sweepLocked() {
	now := s.now()
	if now.Sub(s.lastSweep) < loginPasswordChallengeSweepInterval {
		return
	}
	s.lastSweep = now
	for id, e := range s.m {
		if !now.Before(e.c.ExpiresAt) {
			s.removeLocked(id)
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
