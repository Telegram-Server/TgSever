package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AccountLifecycleStore 是 store.AccountLifecycleStore 的内存实现，只覆盖立即注销：
// 写墓碑（与 postgres 的 ExecuteAccountDeletion 一样清掉号码、名字、用户名等资料，
// 账号行保留供历史消息渲染 Deleted Account）并删除登录密码，两者在同一次持锁中完成；
// 随后撤销该账号的全部授权。
//
// 内存实现不记录 2FA 密码的修改时间（PasswordUpdatedAt 恒为零），account.DeleteAccount
// 因此永远走立即注销分支，不会请求七天的延迟注销；ScheduleAccountDeletion 明确拒绝，
// 待注销请求相关的查询都返回"没有"。
type AccountLifecycleStore struct {
	users          *UserStore
	loginPasswords *LoginPasswordStore
	passwords      accountLifecyclePasswords
	authorizations accountLifecycleAuthorizations
}

var _ store.AccountLifecycleStore = (*AccountLifecycleStore)(nil)

// accountLifecyclePasswords 读取 2FA 云密码是否已设置（快照里的 HasPassword）。
type accountLifecyclePasswords interface {
	GetByUser(ctx context.Context, userID int64) (domain.PasswordSettings, bool, error)
}

// accountLifecycleAuthorizations 撤销被注销账号的全部授权。
type accountLifecycleAuthorizations interface {
	RevokeByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

// accountLifecycleDeviceBindings 删除被注销账号的设备绑定名单（memory.AuthorizationStore 实现）。
type accountLifecycleDeviceBindings interface {
	DeleteDeviceBindings(ctx context.Context, userID int64) error
}

// AccountLifecycleOption 调整内存账号生命周期 store。
type AccountLifecycleOption func(*AccountLifecycleStore)

// WithAccountLifecyclePasswords 注入 2FA 云密码 store，快照据此报告 HasPassword。
func WithAccountLifecyclePasswords(passwords accountLifecyclePasswords) AccountLifecycleOption {
	return func(s *AccountLifecycleStore) { s.passwords = passwords }
}

// WithAccountLifecycleAuthorizations 注入授权 store，注销时撤销该账号的全部授权。
func WithAccountLifecycleAuthorizations(authorizations accountLifecycleAuthorizations) AccountLifecycleOption {
	return func(s *AccountLifecycleStore) { s.authorizations = authorizations }
}

// NewAccountLifecycleStore 创建与 users、loginPasswords 共享数据的内存账号生命周期 store。
// loginPasswords 可以为 nil（不启用登录密码时）。
func NewAccountLifecycleStore(users *UserStore, loginPasswords *LoginPasswordStore, opts ...AccountLifecycleOption) *AccountLifecycleStore {
	s := &AccountLifecycleStore{users: users, loginPasswords: loginPasswords}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *AccountLifecycleStore) AccountDeletionSnapshot(ctx context.Context, userID int64) (domain.AccountDeletionSnapshot, bool, error) {
	if s == nil || s.users == nil || userID == 0 {
		return domain.AccountDeletionSnapshot{}, false, nil
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil || !found {
		return domain.AccountDeletionSnapshot{}, found, err
	}
	snapshot := domain.AccountDeletionSnapshot{User: u}
	if s.passwords != nil {
		settings, found, err := s.passwords.GetByUser(ctx, userID)
		if err != nil {
			return domain.AccountDeletionSnapshot{}, false, err
		}
		snapshot.HasPassword = found && settings.HasPassword
	}
	return snapshot, true, nil
}

// ScheduleAccountDeletion 内存实现不支持延迟注销（见类型注释）。
func (s *AccountLifecycleStore) ScheduleAccountDeletion(context.Context, domain.ScheduleAccountDeletion) (domain.AccountDeletionRequest, bool, error) {
	return domain.AccountDeletionRequest{}, false, domain.ErrAccountDeletionForbidden
}

func (s *AccountLifecycleStore) PendingAccountDeletionByHash(context.Context, int64, [32]byte) (domain.AccountDeletionRequest, bool, error) {
	return domain.AccountDeletionRequest{}, false, nil
}

func (s *AccountLifecycleStore) CancelAccountDeletion(context.Context, int64, [32]byte, time.Time) ([]domain.Authorization, error) {
	return nil, domain.ErrAccountDeletionHashInvalid
}

func (s *AccountLifecycleStore) DueAccountDeletions(context.Context, time.Time, int) ([]domain.AccountDeletionCandidate, error) {
	return nil, nil
}

// validMemoryAccountDeletionSource 与 postgres 的 validAccountDeletionSource 接受同一组来源。
func validMemoryAccountDeletionSource(source domain.AccountDeletionSource) bool {
	switch source {
	case domain.AccountDeletionManual, domain.AccountDeletionForgotPassword, domain.AccountDeletionTOSDecline,
		domain.AccountDeletionPasswordResetExpiry, domain.AccountDeletionAccountTTL, domain.AccountDeletionFreezeExpiry:
		return true
	default:
		return false
	}
}

// ExecuteAccountDeletion 写墓碑并删除登录密码。加锁顺序与 LoginPasswordStore 一致：
// users.mu → loginPasswords.mu，所以写墓碑和删密码之间没有别人能看到中间态。
func (s *AccountLifecycleStore) ExecuteAccountDeletion(ctx context.Context, userID int64, source domain.AccountDeletionSource, reason string, now time.Time) (domain.AccountDeletionResult, error) {
	if s == nil || s.users == nil || userID == 0 || now.IsZero() || !validMemoryAccountDeletionSource(source) {
		return domain.AccountDeletionResult{}, domain.ErrAccountDeletionForbidden
	}
	s.users.mu.Lock()
	u, found := s.users.byID[userID]
	if !found {
		s.users.mu.Unlock()
		return domain.AccountDeletionResult{}, domain.ErrUserNotFound
	}
	if u.Deleted {
		s.users.mu.Unlock()
		return domain.AccountDeletionResult{User: u, Changed: false}, nil
	}
	if u.Bot || domain.IsSystemUserID(u.ID) {
		s.users.mu.Unlock()
		return domain.AccountDeletionResult{}, domain.ErrAccountDeletionForbidden
	}
	tombstone := domain.User{
		ID:         u.ID,
		AccessHash: u.AccessHash,
		CreatedAt:  u.CreatedAt,
		Deleted:    true,
		DeletedAt:  now.Unix(),
	}
	if business, ok := s.passwords.(interface {
		DeleteBusinessConnectionsForUser(context.Context, int64) error
	}); ok {
		if err := business.DeleteBusinessConnectionsForUser(ctx, userID); err != nil {
			s.users.mu.Unlock()
			return domain.AccountDeletionResult{}, err
		}
	}
	s.users.byID[userID] = tombstone
	if s.loginPasswords != nil {
		s.loginPasswords.mu.Lock()
		delete(s.loginPasswords.byUser, userID)
		s.loginPasswords.mu.Unlock()
	}
	s.users.mu.Unlock()

	result := domain.AccountDeletionResult{User: tombstone, Changed: true}
	// 设备绑定名单随账号一起删除（#112）；退出登录、撤销授权都不删它，所以单独调用。
	if bindings, ok := s.authorizations.(accountLifecycleDeviceBindings); ok {
		if err := bindings.DeleteDeviceBindings(ctx, userID); err != nil {
			return domain.AccountDeletionResult{}, err
		}
	}
	if s.authorizations != nil {
		revoked, err := s.authorizations.RevokeByUserExcept(ctx, userID, [8]byte{})
		if err != nil {
			return domain.AccountDeletionResult{}, err
		}
		result.RevokedAuthorizations = revoked
	}
	return result, nil
}
