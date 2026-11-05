package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// GuestSignUpService 抽象游客注册（app/guestsignup，telesrv 扩展协议）。
type GuestSignUpService interface {
	// SignUp 处理 telesrv.signUpAsGuest：clientIP 是连接的对端 IP。同一 IP 在绑定天数内绑着
	// 未转正的账号时登录它，否则建新账号；两种情况都绑定当前授权。复用的账号开了 2FA（并发
	// 竞争下才可能）时返回用户和 domain.ErrSessionPasswordNeeded。
	SignUp(ctx context.Context, a domain.Authorization, clientIP, firstName, lastName string) (domain.User, error)
}

func (r *Router) registerTelesrvGuest(d *tlprofile.Dispatcher) {
	registerRPC[*tg.TelesrvSignUpAsGuestRequest](d, tlprofile.SemanticMethodTelesrvSignUpAsGuest, func(ctx context.Context, req *tg.TelesrvSignUpAsGuestRequest) (any, error) {
		return r.onTelesrvSignUpAsGuest(ctx, req)
	})
}

// onTelesrvSignUpAsGuest 处理 telesrv.signUpAsGuest（未登录可调用）。收尾与密码登录相同
// （finishAuthSignIn）：授权缓存 → session 绑定；复用已有账号时给它其它在线会话推新设备登录
// 通知，新建账号没有其它设备。
func (r *Router) onTelesrvSignUpAsGuest(ctx context.Context, req *tg.TelesrvSignUpAsGuestRequest) (tg.AuthAuthorizationClass, error) {
	if r.deps.GuestSignUp == nil {
		return nil, guestLoginDisabledErr()
	}
	clientIP, _ := ClientIPFrom(ctx)
	u, err := r.deps.GuestSignUp.SignUp(ctx, r.authzFromCtx(ctx), clientIP, req.FirstName, req.LastName)
	if err != nil && !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		return nil, guestSignUpErr(err)
	}
	return r.finishAuthSignIn(ctx, u, false, err)
}

func guestLoginDisabledErr() error { return tgerr.New(400, "GUEST_LOGIN_DISABLED") }

// guestSignUpErr 映射游客注册的业务错误；名字、授权绑定等沿用登录注册（signInErr）。
func guestSignUpErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrGuestLoginDisabled):
		return guestLoginDisabledErr()
	case errors.Is(err, domain.ErrGuestIPUnavailable):
		return tgerr.New(400, "GUEST_IP_UNAVAILABLE")
	case errors.Is(err, domain.ErrGuestNumberPoolExhausted):
		return tgerr.New(500, "GUEST_NUMBER_POOL_EXHAUSTED")
	default:
		return signInErr(err)
	}
}
