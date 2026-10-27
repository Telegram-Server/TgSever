package auth

import (
	"context"
	"errors"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PermissionDefaultsReader 读取默认权限模板（store.PermissionDefaultsStore 的读取部分）。
// found=false 表示没有模板。
type PermissionDefaultsReader interface {
	GetPermissionDefaults(ctx context.Context) (d domain.PermissionDefaults, found bool, err error)
}

// SignUpAccountFunc 是自助注册的建号步骤：把新账号写入存储，返回带 ID 的用户。permissions
// 非 nil 时是要套用的默认权限快照，必须与账号在同一个事务里写入
// （store.AccountExtras.Permissions），保证账号从诞生起就受控。号码已被占用时应返回
// domain.ErrPhoneNumberOccupied。
type SignUpAccountFunc func(ctx context.Context, u domain.User, permissions *domain.AccountPermissions) (domain.User, error)

// WithAccountCreation 让验证码注册经原子建号操作建号，以便与默认权限快照一起写入。
// 不设置时验证码注册退回普通建号（此时套用模板会失败，见 selfSignUpPermissions）。
func WithAccountCreation(accounts store.AccountCreationStore) Option {
	return func(s *Service) {
		s.accounts = accounts
	}
}

// WithSelfSignUpPermissionDefaults 让自助注册（验证码注册、密码注册）在模板的
// "自助注册也套用"开启时套用默认权限模板（#163）。每次注册读一次，不缓存。
// 不设置时自助注册不写权限。后台建号与 bot 不经过这里。
func WithSelfSignUpPermissionDefaults(defaults PermissionDefaultsReader) Option {
	return func(s *Service) {
		s.selfSignUpDefaults = defaults
	}
}

// selfSignUpPermissions 返回这次自助注册要套用的权限快照：没有模板或开关关闭时为 nil。
// 读取失败时返回错误，调用方必须不建号。
func (s *Service) selfSignUpPermissions(ctx context.Context) (*domain.AccountPermissions, error) {
	if s.selfSignUpDefaults == nil {
		return nil, nil
	}
	d, found, err := s.selfSignUpDefaults.GetPermissionDefaults(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: read permission defaults for sign up: %w", err)
	}
	if !found || !d.ApplyToSelfSignup {
		return nil, nil
	}
	return d.Snapshot(""), nil
}

// createSelfSignUpUser 是验证码注册的建号：配置了原子建号操作时经它建号（带权限快照），
// 否则普通建号。有权限快照却没有原子建号操作是装配错误，拒绝建号。
func (s *Service) createSelfSignUpUser(ctx context.Context, u domain.User, permissions *domain.AccountPermissions) (domain.User, error) {
	if s.accounts != nil {
		return s.accounts.CreateAccount(ctx, u, store.AccountExtras{Permissions: permissions})
	}
	if permissions != nil {
		return domain.User{}, errors.New("auth: permission defaults apply to sign up but no account creation store is configured")
	}
	return s.users.Create(ctx, u)
}
