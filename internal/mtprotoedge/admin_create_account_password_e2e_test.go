package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/admin"
)

// TestAdminCreateAccountWithPasswordEndToEnd 只验证接线：经后台建号命令（进程内接上服务端的
// auth 服务和登录密码服务，生产里经 admin API 调同一个方法）建一个带登录密码的账号之后，
// 它马上能在线上用 telesrv.signInWithPassword 登录。命令本身的行为矩阵在 internal/admin 里。
func TestAdminCreateAccountWithPasswordEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true})
	const phone = "15550760101"

	adminSvc := admin.NewService(admin.Dependencies{
		Commands:                    &e2eAdminCommands{},
		AccountCreator:              srv.auth,
		LoginPasswords:              srv.loginPasswordService,
		LoginPasswordFingerprintKey: admin.DeriveLoginPasswordFingerprintKey("e2e-admin-token"),
	})
	res, err := adminSvc.CreateAccount(context.Background(), admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "e2e-create-account-password", Actor: "ops", Reason: "new hire"},
		Phone:       "+" + phone,
		FirstName:   "Pat",
		Password:    "first day",
	})
	if err != nil {
		t.Fatalf("admin CreateAccount: result=%+v err=%v", res, err)
	}
	userID, _ := res.Details["user_id"].(int64)

	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		got, err := signInWithPassword(ctx, raw, "+"+phone, "first day")
		if err != nil {
			return fmt.Errorf("telesrv.signInWithPassword: %w", err)
		}
		authz, ok := got.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("signInWithPassword = %T, want *tg.AuthAuthorization", got)
		}
		if user, _ := authz.User.(*tg.User); user == nil || user.ID != userID {
			return fmt.Errorf("signed in as %#v, want user %d", authz.User, userID)
		}
		return nil
	})
}
