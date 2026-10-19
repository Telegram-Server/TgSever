package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// LoginPasswordService 抽象手机号 + 密码登录（app/loginpassword，telesrv 扩展协议）。
// 总开关关闭时每个方法都返回 domain.ErrPasswordLoginDisabled。
type LoginPasswordService interface {
	// Enabled 报告总开关是否打开。handler 在解析任何参数之前先检查它，
	// 保证开关关闭时每个请求都只会得到 PASSWORD_LOGIN_DISABLED。
	Enabled() bool
	// GetLoginPassword 处理 telesrv.getLoginPassword。号码没有账号时返回注册用的
	// account.password 参数；已有账号时返回 domain.ErrPasswordLoginNotSet（#31 过渡行为）。
	GetLoginPassword(ctx context.Context, phone string) (domain.PasswordSettings, error)
	// SignUp 处理 telesrv.signUpWithPassword：建号并写入登录密码，绑定当前授权。
	SignUp(ctx context.Context, a domain.Authorization, phone, firstName, lastName string, settings domain.PasswordInputSettings) (domain.User, error)
}

// registerTelesrv 注册 telesrv.* 扩展方法（td 的 telesrv overlay，见 _schema/TELESRV_EXTENSIONS.md）。
func (r *Router) registerTelesrv(d *tlprofile.Dispatcher) {
	registerRPC[*tg.TelesrvGetLoginPasswordRequest](d, tlprofile.SemanticMethodTelesrvGetLoginPassword, func(ctx context.Context, req *tg.TelesrvGetLoginPasswordRequest) (any, error) {
		return r.onTelesrvGetLoginPassword(ctx, req)
	})
	registerRPC[*tg.TelesrvSignUpWithPasswordRequest](d, tlprofile.SemanticMethodTelesrvSignUpWithPassword, func(ctx context.Context, req *tg.TelesrvSignUpWithPasswordRequest) (any, error) {
		return r.onTelesrvSignUpWithPassword(ctx, req)
	})
}

// onTelesrvGetLoginPassword 处理 telesrv.getLoginPassword（未登录可调用）。
// 返回值复用 account.password 的转换，new_algo / new_secure_algo / secure_random 都会填上。
func (r *Router) onTelesrvGetLoginPassword(ctx context.Context, req *tg.TelesrvGetLoginPasswordRequest) (*tg.AccountPassword, error) {
	if !r.passwordLoginEnabled() {
		return nil, passwordLoginDisabledErr()
	}
	settings, err := r.deps.LoginPassword.GetLoginPassword(ctx, req.PhoneNumber)
	if err != nil {
		return nil, loginPasswordErr(err)
	}
	return tgPassword(settings), nil
}

// onTelesrvSignUpWithPassword 处理 telesrv.signUpWithPassword（未登录可调用）。
// 收尾与 auth.signUp 相同：授权缓存 → session 绑定 → 返回 auth.authorization。
// 新账号没有其它设备，不推新登录通知；也没有 bootstrap 登录码消息要入队。
func (r *Router) onTelesrvSignUpWithPassword(ctx context.Context, req *tg.TelesrvSignUpWithPasswordRequest) (tg.AuthAuthorizationClass, error) {
	if !r.passwordLoginEnabled() {
		return nil, passwordLoginDisabledErr()
	}
	settings, err := domainPasswordInputSettings(req.NewSettings)
	if err != nil {
		return nil, err
	}
	u, err := r.deps.LoginPassword.SignUp(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.FirstName, req.LastName, settings)
	if err != nil {
		return nil, loginPasswordErr(err)
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
}

// passwordLoginEnabled 报告密码登录是否可用：服务未注入与总开关关闭等价。
func (r *Router) passwordLoginEnabled() bool {
	return r.deps.LoginPassword != nil && r.deps.LoginPassword.Enabled()
}

func passwordLoginDisabledErr() error { return tgerr.New(403, "PASSWORD_LOGIN_DISABLED") }
func passwordLoginNotSetErr() error   { return tgerr.New(400, "PASSWORD_LOGIN_NOT_SET") }

// loginPasswordErr 映射登录密码业务错误：开关与"未设登录密码"是本功能特有的；
// new_settings 的错误与 2FA 设置密码一致（passwordErr）；号码、名字、占用等沿用登录注册（signInErr）。
func loginPasswordErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPasswordLoginDisabled):
		return passwordLoginDisabledErr()
	case errors.Is(err, domain.ErrPasswordLoginNotSet):
		return passwordLoginNotSetErr()
	case errors.Is(err, domain.ErrNewSettingsInvalid),
		errors.Is(err, domain.ErrNewSaltInvalid),
		errors.Is(err, domain.ErrPasswordHashInvalid):
		return passwordErr(err)
	default:
		return signInErr(err)
	}
}
