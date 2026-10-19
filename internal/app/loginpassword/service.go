// Package loginpassword 实现手机号 + 密码登录（telesrv 扩展协议，#27）的业务逻辑：
// 查询注册/登录参数、密码注册，以及后续票的密码登录与修改。
//
// 登录密码与 2FA 云密码互相独立，各存各的；SRP 数学与 2FA 共用 internal/srp。
// 整个功能由总开关控制，关闭时每个方法都返回 domain.ErrPasswordLoginDisabled。
package loginpassword

import (
	"context"
	"errors"

	"telesrv/internal/app/account"
	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store"
)

// SignUpCompleter 是注册的后半段（auth.Service.SignUpWithAccountCreator）：按 auth.signUp 的
// 规则校验号码和名字，用 create 建号，然后赠送会员、绑定授权、写欢迎消息。
type SignUpCompleter interface {
	SignUpWithAccountCreator(ctx context.Context, a domain.Authorization, phone, firstName, lastName string, create auth.CreateAccountFunc) (domain.User, error)
}

// Service 是登录密码业务服务。
type Service struct {
	enabled   bool
	users     store.UserStore
	passwords store.LoginPasswordStore
	signUp    SignUpCompleter
}

// Option 调整登录密码服务。
type Option func(*Service)

// WithEnabled 设置总开关（TELESRV_PASSWORD_LOGIN_ENABLED），默认关闭。
func WithEnabled(enabled bool) Option {
	return func(s *Service) {
		s.enabled = enabled
	}
}

// NewService 创建登录密码服务。users 用来按号码查账号，passwords 保存登录密码，
// signUp 提供与验证码注册共用的注册后半段。
func NewService(users store.UserStore, passwords store.LoginPasswordStore, signUp SignUpCompleter, opts ...Option) *Service {
	s := &Service{users: users, passwords: passwords, signUp: signUp}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Enabled 报告总开关是否打开，并且依赖齐全。
func (s *Service) Enabled() bool {
	return s != nil && s.enabled && s.users != nil && s.passwords != nil && s.signUp != nil
}

// GetLoginPassword 处理 telesrv.getLoginPassword：号码没有账号时返回注册用的参数
// （has_password=false、new_algo、new_secure_algo、secure_random），客户端据此进入注册。
//
// 号码已有账号时统一返回 domain.ErrPasswordLoginNotSet。这是 #31 的过渡行为：
// 已设登录密码的分支（has_password=true，带 current_algo、srp_B、srp_id）由 #32 补上。
func (s *Service) GetLoginPassword(ctx context.Context, phone string) (domain.PasswordSettings, error) {
	if !s.Enabled() {
		return domain.PasswordSettings{}, domain.ErrPasswordLoginDisabled
	}
	phone, err := auth.NormalizeSignUpPhone(phone)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	_, found, err := s.users.ByPhone(ctx, phone)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	if found {
		// TODO(#32): 账号设了登录密码时返回 has_password=true 和 SRP challenge。
		return domain.PasswordSettings{}, domain.ErrPasswordLoginNotSet
	}
	return account.NoPasswordSettings(), nil
}

// SignUp 处理 telesrv.signUpWithPassword：不经验证码，用手机号和登录密码直接注册，
// 成功后当前授权立即处于登录状态。校验顺序：开关 → 号码 → 名字 → new_settings。
// 建号和写入登录密码在同一个事务里完成；号码已被占用返回 domain.ErrPhoneNumberOccupied。
func (s *Service) SignUp(ctx context.Context, a domain.Authorization, phone, firstName, lastName string, settings domain.PasswordInputSettings) (domain.User, error) {
	if !s.Enabled() {
		return domain.User{}, domain.ErrPasswordLoginDisabled
	}
	phone, err := auth.NormalizeSignUpPhone(phone)
	if err != nil {
		return domain.User{}, err
	}
	firstName, lastName, err = auth.NormalizeSignUpName(firstName, lastName)
	if err != nil {
		return domain.User{}, err
	}
	password, err := newLoginPassword(settings)
	if err != nil {
		return domain.User{}, err
	}
	return s.signUp.SignUpWithAccountCreator(ctx, a, phone, firstName, lastName, func(ctx context.Context, u domain.User) (domain.User, error) {
		return s.passwords.CreateUserWithLoginPassword(ctx, u, password)
	})
}

// newLoginPassword 校验客户端提交的 new_settings 并转换成要保存的登录密码。
// 只读 new_algo 和 new_password_hash：带了 email 一律拒绝（登录密码没有找回邮箱），
// hint 忽略。盐和算法的规则与 2FA 设置密码时完全一样（internal/srp.ValidateNewPassword）。
func newLoginPassword(in domain.PasswordInputSettings) (domain.LoginPassword, error) {
	if in.HasEmail {
		return domain.LoginPassword{}, domain.ErrNewSettingsInvalid
	}
	var algo *srp.Algo
	if in.NewAlgo != nil {
		algo = &srp.Algo{Salt1: in.NewAlgo.Salt1, Salt2: in.NewAlgo.Salt2, G: in.NewAlgo.G, P: in.NewAlgo.P}
	}
	if err := srp.ValidateNewPassword(algo, in.NewPasswordHash); err != nil {
		return domain.LoginPassword{}, mapNewPasswordError(err)
	}
	verifier, err := srp.Pad(in.NewPasswordHash)
	if err != nil {
		return domain.LoginPassword{}, domain.ErrPasswordHashInvalid
	}
	return domain.LoginPassword{
		Salt1:    append([]byte(nil), algo.Salt1...),
		Salt2:    append([]byte(nil), algo.Salt2...),
		Verifier: verifier,
	}, nil
}

// mapNewPasswordError 把 srp 包校验新密码的错误映射成与 2FA 一致的 domain 错误。
func mapNewPasswordError(err error) error {
	switch {
	case errors.Is(err, srp.ErrNewSettingsMissing):
		return domain.ErrNewSettingsInvalid
	case errors.Is(err, srp.ErrNewAlgoInvalid):
		return domain.ErrNewSaltInvalid
	case errors.Is(err, srp.ErrInvalidVerifier):
		return domain.ErrPasswordHashInvalid
	default:
		return err
	}
}
