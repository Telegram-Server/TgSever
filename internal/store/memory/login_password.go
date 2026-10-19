package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// LoginPasswordStore 是 store.LoginPasswordStore 的内存实现。它和传入的 UserStore 共享账号：
// 写入前确认账号存在，建号并写密码时在同一次持锁中完成。
//
// 加锁顺序固定为 users.mu → s.mu。只需要账号信息的路径（Set、Create）先拿 users.mu，
// 只读写密码的路径（Get、Delete）只拿 s.mu，从不反向嵌套，所以不会死锁。
type LoginPasswordStore struct {
	users *UserStore

	mu     sync.RWMutex
	byUser map[int64]domain.LoginPassword
}

var _ store.LoginPasswordStore = (*LoginPasswordStore)(nil)

// NewLoginPasswordStore 创建与 users 共享账号的内存登录密码存储。
func NewLoginPasswordStore(users *UserStore) *LoginPasswordStore {
	return &LoginPasswordStore{users: users, byUser: make(map[int64]domain.LoginPassword)}
}

func (s *LoginPasswordStore) GetLoginPassword(_ context.Context, userID int64) (domain.LoginPassword, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pw, ok := s.byUser[userID]
	if !ok {
		return domain.LoginPassword{}, false, nil
	}
	return cloneLoginPassword(pw), true, nil
}

func (s *LoginPasswordStore) SetLoginPassword(_ context.Context, userID int64, password domain.LoginPassword) error {
	if !loginPasswordComplete(password) {
		return store.ErrLoginPasswordInvalid
	}
	// 与 postgres 的外键一致：只要求账号行存在（软删除的账号也有行）。
	s.users.mu.RLock()
	defer s.users.mu.RUnlock()
	if _, ok := s.users.byID[userID]; !ok {
		return domain.ErrUserNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putLocked(userID, password, time.Now().UTC())
	return nil
}

func (s *LoginPasswordStore) DeleteLoginPassword(_ context.Context, userID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byUser[userID]; !ok {
		return false, nil
	}
	delete(s.byUser, userID)
	return true, nil
}

// CreateUserWithLoginPassword 持有 users.mu 写锁和 s.mu 写锁完成建号和写密码。密码材料在
// 建号之前校验，createLocked 失败时不改动任何状态，写密码这一步本身不会失败，所以不会出现
// 有账号没密码的中间态；号码唯一性检查和插入在同一把 users.mu 里，并发注册恰好一个成功。
func (s *LoginPasswordStore) CreateUserWithLoginPassword(_ context.Context, u domain.User, password domain.LoginPassword) (domain.User, error) {
	if !loginPasswordComplete(password) {
		return domain.User{}, store.ErrLoginPasswordInvalid
	}
	s.users.mu.Lock()
	defer s.users.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	created, err := s.users.createLocked(u)
	if err != nil {
		return domain.User{}, err
	}
	s.putLocked(created.ID, password, time.Now().UTC())
	return created, nil
}

// putLocked 写入或替换密码，保留首次设置的 CreatedAt。调用方持有 s.mu 写锁。
func (s *LoginPasswordStore) putLocked(userID int64, password domain.LoginPassword, now time.Time) {
	next := cloneLoginPassword(password)
	next.CreatedAt = now
	if existing, ok := s.byUser[userID]; ok {
		next.CreatedAt = existing.CreatedAt
	}
	next.UpdatedAt = now
	s.byUser[userID] = next
}

// loginPasswordComplete 对应 postgres 的 login_passwords_material_check。
func loginPasswordComplete(pw domain.LoginPassword) bool {
	return len(pw.Salt1) > 0 && len(pw.Salt2) > 0 && len(pw.Verifier) > 0
}

func cloneLoginPassword(pw domain.LoginPassword) domain.LoginPassword {
	pw.Salt1 = append([]byte(nil), pw.Salt1...)
	pw.Salt2 = append([]byte(nil), pw.Salt2...)
	pw.Verifier = append([]byte(nil), pw.Verifier...)
	return pw
}
