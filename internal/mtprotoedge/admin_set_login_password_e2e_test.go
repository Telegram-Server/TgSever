package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// TestAdminSetLoginPasswordEndToEnd 只验证接线：给一个用验证码注册、没有登录密码的账号，经后台
// 命令（进程内接上服务端的登录密码服务，生产里经 admin API 调同一个方法）设置密码之后，
// 它能在线上用 telesrv.signInWithPassword 登录。命令本身的行为矩阵在 internal/admin 里。
func TestAdminSetLoginPasswordEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true})
	const phone = "15550760001"
	u, err := srv.users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Code"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	adminSvc := admin.NewService(admin.Dependencies{
		Commands:                    &e2eAdminCommands{},
		LoginPasswords:              srv.loginPasswordService,
		LoginPasswordFingerprintKey: admin.DeriveLoginPasswordFingerprintKey("e2e-admin-token"),
	})
	res, err := adminSvc.SetLoginPassword(context.Background(), admin.SetLoginPasswordRequest{
		CommandMeta: admin.CommandMeta{CommandID: "e2e-set-login-password", Actor: "ops", Reason: "switch to password login"},
		UserID:      u.ID,
		Password:    "set by ops",
	})
	if err != nil {
		t.Fatalf("admin SetLoginPassword: result=%+v err=%v", res, err)
	}

	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		got, err := signInWithPassword(ctx, raw, "+"+phone, "set by ops")
		if err != nil {
			return fmt.Errorf("telesrv.signInWithPassword: %w", err)
		}
		authz, ok := got.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("signInWithPassword = %T, want *tg.AuthAuthorization", got)
		}
		if user, _ := authz.User.(*tg.User); user == nil || user.ID != u.ID {
			return fmt.Errorf("signed in as %#v, want user %d", authz.User, u.ID)
		}
		return nil
	})
}
