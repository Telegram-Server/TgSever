package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestSelfSignUpPermissionDefaultsEndToEnd 只验证接线（#163）：默认权限模板的"自助注册也套用"
// 开启时，客户端自己用验证码注册的新账号，第一次拉取的 appConfig 里就有 telesrv_permissions，
// 值与模板一致。两条注册路径 × 开关 × 模板有无的矩阵在 internal/app/auth 与
// internal/app/loginpassword。
func TestSelfSignUpPermissionDefaultsEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	if _, _, _, err := srv.accountPermissions.UpdatePermissionDefaults(context.Background(), func(domain.PermissionDefaults, bool) domain.PermissionDefaults {
		d := domain.DefaultPermissionDefaults()
		d.Enabled = true
		d.Flags.Edit = false
		d.Flags.CreateChannel = false
		d.ApplyToSelfSignup = true
		return d
	}); err != nil {
		t.Fatalf("write permission defaults: %v", err)
	}

	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		if _, err := signUpWithCode(ctx, raw, "+15550163801", "Staff"); err != nil {
			return fmt.Errorf("sign up: %w", err)
		}
		perms, err := appConfigPermissions(ctx, raw)
		if err != nil {
			return err
		}
		if perms == nil || perms["edit"] != false || perms["create_channel"] != false || perms["send"] != true {
			return fmt.Errorf("telesrv_permissions = %v, want the template (edit=false create_channel=false send=true)", perms)
		}
		return nil
	})
}
