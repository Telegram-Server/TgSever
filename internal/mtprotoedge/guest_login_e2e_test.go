package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/app/guestsignup"
)

// 游客登录的端到端测试只证明接线：telesrv.signUpAsGuest 在登录前可调用、带着连接的对端 IP
// 走到业务层、登录收尾把会话置为已授权，app config 下发开关。功能矩阵（IP 绑定、转正、换号、
// 默认名）在 internal/app/guestsignup 里测。

func TestGuestLoginEndToEnd(t *testing.T) {
	t.Parallel()
	// 注册关闭：游客注册照样能用。
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{guestLogin: true, signUpDisabled: true})

	var firstID int64
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		value, ok, err := appConfigValue(ctx, raw, "telesrv_guest_login_enabled")
		if err != nil {
			return err
		}
		if b, isBool := value.(*tg.JSONBool); !ok || !isBool || !b.Value {
			return fmt.Errorf("telesrv_guest_login_enabled = %#v (present=%v), want true", value, ok)
		}
		res, err := raw.TelesrvSignUpAsGuest(ctx, &tg.TelesrvSignUpAsGuestRequest{})
		if err != nil {
			return fmt.Errorf("telesrv.signUpAsGuest: %w", err)
		}
		authorization, ok := res.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("telesrv.signUpAsGuest = %T, want auth.authorization", res)
		}
		self, ok := authorization.User.(*tg.User)
		if !ok || !self.Self || !guestsignup.IsGuestNumber(self.Phone) || self.FirstName == "" {
			return fmt.Errorf("user = %#v, want self with a guest number and a generated name", authorization.User)
		}
		firstID = self.ID
		// 会话已授权：需要登录的 RPC 能用。
		users, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil || len(users) != 1 {
			return fmt.Errorf("users.getUsers(self) after guest sign-up = %v, %v", users, err)
		}
		return nil
	})

	// 新设备、同一 IP（127.0.0.1）：登录的是同一个游客账号。
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		res, err := raw.TelesrvSignUpAsGuest(ctx, &tg.TelesrvSignUpAsGuestRequest{FirstName: "Other"})
		if err != nil {
			return fmt.Errorf("second telesrv.signUpAsGuest: %w", err)
		}
		self, ok := res.(*tg.AuthAuthorization).User.(*tg.User)
		if !ok || self.ID != firstID {
			return fmt.Errorf("second guest sign-up user = %#v, want the same account %d", res, firstID)
		}
		return nil
	})
}

func TestGuestLoginDisabledEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		if _, ok, err := appConfigValue(ctx, raw, "telesrv_guest_login_enabled"); err != nil || ok {
			return fmt.Errorf("telesrv_guest_login_enabled present=%v err=%v while guest login is off", ok, err)
		}
		_, err := raw.TelesrvSignUpAsGuest(ctx, &tg.TelesrvSignUpAsGuestRequest{})
		if !tgerr.Is(err, "GUEST_LOGIN_DISABLED") {
			return fmt.Errorf("telesrv.signUpAsGuest err = %v, want GUEST_LOGIN_DISABLED", err)
		}
		return nil
	})
}
