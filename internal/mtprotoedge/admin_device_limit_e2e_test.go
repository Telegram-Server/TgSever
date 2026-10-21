package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/admin"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
)

// TestAdminDeviceLimitEndToEnd 只验证接线：经后台命令（进程内接上服务端的授权 store，生产里经
// admin API 调同一个方法）给账号设上限 1 之后，第一台设备能用验证码登录，第二台设备登录时客户端
// 收到 AUTHORIZATIONS_TOO_MUCH。上限的行为矩阵在 internal/app/auth 与 storetest 里。
func TestAdminDeviceLimitEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	const phone = "15550105301"
	u, err := srv.users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Staff"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	adminSvc := admin.NewService(admin.Dependencies{
		Commands:     &e2eAdminCommands{},
		Users:        users.NewService(srv.users),
		DeviceLimits: srv.authorizations,
	})
	limit := 1
	res, err := adminSvc.SetDeviceLimit(context.Background(), admin.SetDeviceLimitRequest{
		CommandMeta: admin.CommandMeta{CommandID: "e2e-set-device-limit", Actor: "ops", Reason: "staff account"},
		UserID:      u.ID,
		DeviceLimit: &limit,
	})
	if err != nil {
		t.Fatalf("admin SetDeviceLimit: result=%+v err=%v", res, err)
	}

	signIn := func(ctx context.Context, raw *tg.Client) (tg.AuthAuthorizationClass, error) {
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+" + phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return nil, fmt.Errorf("auth.sendCode: %w", err)
		}
		return raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "+" + phone, PhoneCodeHash: sent.(*tg.AuthSentCode).PhoneCodeHash, PhoneCode: loginPasswordTestCode})
	}
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		if _, err := signIn(ctx, raw); err != nil {
			return fmt.Errorf("first device auth.signIn: %w", err)
		}
		return nil
	})
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		_, err := signIn(ctx, raw)
		if !tgerr.Is(err, "AUTHORIZATIONS_TOO_MUCH") {
			return fmt.Errorf("second device auth.signIn err=%v, want AUTHORIZATIONS_TOO_MUCH", err)
		}
		return nil
	})
}
