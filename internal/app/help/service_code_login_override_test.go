package help

import (
	"context"
	"testing"

	"telesrv/internal/app/auth"
	"telesrv/internal/identity"
)

// 管理后台在 identity 存储里切换验证码登录开关后，getAppConfig 立即反映新状态，hash
// 随之变化；清除覆盖后回到跟随环境变量的配置与 hash。接线与 cmd/telesrv 相同：
// help 读 auth.CodeLoginAllowed，auth 现读 identity 存储。
func TestAppConfigHashChangesAfterAdminCodeLoginOverride(t *testing.T) {
	ctx := context.Background()
	store := identity.NewStore(t.TempDir())
	authService := auth.NewService(nil, nil, nil, nil, nil, "12345",
		auth.WithCodeLoginAllowed(auth.IdentityCodeLoginAllowed(store, true)))
	svc := NewService(nil, nil, WithCodeLoginAllowed(authService.CodeLoginAllowed))

	open, _, err := svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := appConfigValues(t, open)["telesrv_code_login_disabled"]; ok {
		t.Fatal("telesrv_code_login_disabled present while env opens code login and no override is set")
	}

	off := false
	if err := store.SetCodeLoginOverride(&off); err != nil {
		t.Fatal(err)
	}
	closed, notModified, err := svc.GetAppConfig(ctx, 0, open.Hash)
	if err != nil || notModified {
		t.Fatalf("after admin override off notModified=%v err=%v, want a refreshed config", notModified, err)
	}
	if v := appConfigValues(t, closed)["telesrv_code_login_disabled"]; v != true {
		t.Fatalf("telesrv_code_login_disabled = %#v after admin override off, want true", v)
	}
	if closed.Hash == open.Hash {
		t.Fatalf("hash %d unchanged after admin override off", closed.Hash)
	}

	if err := store.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	cleared, notModified, err := svc.GetAppConfig(ctx, 0, closed.Hash)
	if err != nil || notModified || cleared.Hash != open.Hash {
		t.Fatalf("after clearing override hash=%d notModified=%v err=%v, want the env-open hash %d", cleared.Hash, notModified, err, open.Hash)
	}
}
