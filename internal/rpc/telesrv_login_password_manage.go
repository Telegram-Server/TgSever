package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// LoginPasswordManager 是已登录时管理登录密码的两个方法（app/loginpassword）。
// 总开关关闭时都返回 domain.ErrPasswordLoginDisabled。
type LoginPasswordManager interface {
	// GetMyLoginPassword 处理 telesrv.getMyLoginPassword：没有登录密码时返回 has_password=false
	// 和设置用的 new_algo；有登录密码时另带一个只有 rawAuthKeyID 能用的 SRP challenge。
	GetMyLoginPassword(ctx context.Context, rawAuthKeyID [8]byte, userID int64) (domain.PasswordSettings, error)
	// UpdateMyLoginPassword 处理 telesrv.updateLoginPassword：校验 current 后设置、修改或
	// （new_password_hash 为空时）删除登录密码，成功后由 777000 通知这个账号。
	UpdateMyLoginPassword(ctx context.Context, rawAuthKeyID [8]byte, userID int64, current domain.PasswordCheck, settings domain.PasswordInputSettings) error
}

// registerTelesrvLoginPasswordManage 注册已登录才能调用的两个登录密码方法。它们不在
// rpcAllowedWithoutAuthorization 里：未登录、以及 2FA password_pending 的会话（session 未
// 授权）都在鉴权门口得到 AUTH_KEY_UNREGISTERED，handler 里再按已授权的 user_id 兜一次底。
func (r *Router) registerTelesrvLoginPasswordManage(d *tlprofile.Dispatcher) {
	registerRPC[*tg.TelesrvGetMyLoginPasswordRequest](d, tlprofile.SemanticMethodTelesrvGetMyLoginPassword, func(ctx context.Context, req *tg.TelesrvGetMyLoginPasswordRequest) (any, error) {
		return r.onTelesrvGetMyLoginPassword(ctx, req)
	})
	registerRPC[*tg.TelesrvUpdateLoginPasswordRequest](d, tlprofile.SemanticMethodTelesrvUpdateLoginPassword, func(ctx context.Context, req *tg.TelesrvUpdateLoginPasswordRequest) (any, error) {
		return r.onTelesrvUpdateLoginPassword(ctx, req)
	})
}

// onTelesrvGetMyLoginPassword 处理 telesrv.getMyLoginPassword（必须已登录）。返回值与
// getLoginPassword 一样复用 account.password 的转换；challenge 绑定连接实际使用的原始 auth_key。
func (r *Router) onTelesrvGetMyLoginPassword(ctx context.Context, _ *tg.TelesrvGetMyLoginPasswordRequest) (*tg.AccountPassword, error) {
	if !r.passwordLoginEnabled() {
		return nil, passwordLoginDisabledErr()
	}
	userID, err := r.loginPasswordManageUserID(ctx)
	if err != nil {
		return nil, err
	}
	rawAuthKeyID, _ := RawAuthKeyIDFrom(ctx)
	settings, err := r.deps.LoginPassword.GetMyLoginPassword(ctx, rawAuthKeyID, userID)
	if err != nil {
		return nil, loginPasswordManageErr(err)
	}
	return tgPassword(settings), nil
}

// onTelesrvUpdateLoginPassword 处理 telesrv.updateLoginPassword（必须已登录）。
// 开关检查先于一切参数解析。new_password_hash 为空表示删除：此时不解析 new_algo（客户端
// 删除密码时按惯例发 passwordKdfAlgoUnknown），只保留 email 用于拒绝。
func (r *Router) onTelesrvUpdateLoginPassword(ctx context.Context, req *tg.TelesrvUpdateLoginPasswordRequest) (bool, error) {
	if !r.passwordLoginEnabled() {
		return false, passwordLoginDisabledErr()
	}
	userID, err := r.loginPasswordManageUserID(ctx)
	if err != nil {
		return false, err
	}
	var settings domain.PasswordInputSettings
	if len(req.NewSettings.NewPasswordHash) > 0 {
		if settings, err = domainPasswordInputSettings(req.NewSettings); err != nil {
			return false, err
		}
	} else if email, ok := req.NewSettings.GetEmail(); ok {
		settings.Email, settings.HasEmail = email, true
	}
	rawAuthKeyID, _ := RawAuthKeyIDFrom(ctx)
	if err := r.deps.LoginPassword.UpdateMyLoginPassword(ctx, rawAuthKeyID, userID, domainPasswordCheck(req.Current), settings); err != nil {
		return false, loginPasswordManageErr(err)
	}
	return true, nil
}

// loginPasswordManageUserID 返回已授权会话的 user_id。password_pending 的会话不算已授权。
func (r *Router) loginPasswordManageUserID(ctx context.Context) (int64, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if !authorized || userID == 0 {
		return 0, authKeyUnregisteredErr()
	}
	return userID, nil
}

// loginPasswordManageErr 在 loginPasswordErr 之上处理已登录方法特有的情况：bot（以及不可能
// 持有会话的系统账号、已注销账号）不能管理登录密码，按 bot 调了用户方法处理。
func loginPasswordManageErr(err error) error {
	if errors.Is(err, auth.ErrSystemUserLoginForbidden) {
		return botMethodInvalidErr()
	}
	return loginPasswordErr(err)
}
