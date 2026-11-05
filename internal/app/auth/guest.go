package auth

import (
	"context"
	"errors"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// GuestLoginFunc 报告游客登录的实际设置（开关与 IP 绑定天数）。与 SignUpAllowedFunc 同构：
// 每次判断都实时调用，实现应现读来源、不要自行缓存；返回 error 时按关闭处理（fail closed）。
type GuestLoginFunc func(ctx context.Context) (domain.GuestLoginSettings, error)

// StaticGuestLogin 返回固定结果的 GuestLoginFunc，用于只有环境变量一个来源的部署和测试。
func StaticGuestLogin(enabled bool, days int) GuestLoginFunc {
	settings := domain.ResolveGuestLogin(nil, enabled, nil, days)
	return func(context.Context) (domain.GuestLoginSettings, error) { return settings, nil }
}

// IdentityGuestLogin 返回现读 identity 存储的 GuestLoginFunc：每次判断都读取 identity.json 里的
// 管理面板覆盖值，按 domain.ResolveGuestLogin 与环境变量默认值合并，不缓存。identity.json 读不出
// 时返回错误（fail closed）。nil store 等价于只有环境变量一个来源。help.getAppConfig 经
// IdentityLoginSwitches 下发同一个开关，guestEnv 必须与这里的 envEnabled 相同。
func IdentityGuestLogin(store *identity.Store, envEnabled bool, envDays int) GuestLoginFunc {
	if store == nil {
		return StaticGuestLogin(envEnabled, envDays)
	}
	return func(context.Context) (domain.GuestLoginSettings, error) {
		info, err := store.Get()
		if err != nil {
			return domain.GuestLoginSettings{}, err
		}
		return domain.ResolveGuestLogin(info.GuestLoginEnabled, envEnabled, info.GuestIPBindDays, envDays), nil
	}
}

// SignUpGuestWithAccountCreator 是游客注册的建号：号码和名字按 auth.signUp 的规则校验，之后与
// 密码注册（SignUpWithAccountCreator）走同一段 signUpAccount（access_hash、赠送会员、自助注册
// 默认权限、绑定授权、欢迎消息）。
//
// 它刻意不检查注册开关：游客登录开启时，游客注册绕过"禁止注册"（docs/guest-login.md）。游客
// 开关由调用方（app/guestsignup）检查。验证码登录开关与它无关。
func (s *Service) SignUpGuestWithAccountCreator(ctx context.Context, auth domain.Authorization, phone, firstName, lastName string, create SignUpAccountFunc) (domain.User, error) {
	if s == nil || create == nil {
		return domain.User{}, errors.New("auth: guest sign up without account creator")
	}
	phone, err := NormalizeSignUpPhone(phone)
	if err != nil {
		return domain.User{}, err
	}
	firstName, lastName, err = NormalizeSignUpName(firstName, lastName)
	if err != nil {
		return domain.User{}, err
	}
	// 与密码注册相同：建号会立即提交，先确认当前 auth key 能绑定授权。
	if err := s.checkPermanentAuthKey(ctx, auth.AuthKeyID); err != nil {
		return domain.User{}, err
	}
	return s.signUpAccount(ctx, auth, phone, firstName, lastName, create, nil)
}
