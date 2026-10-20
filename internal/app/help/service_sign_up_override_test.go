package help

import (
	"context"
	"testing"

	"telesrv/internal/app/auth"
	"telesrv/internal/identity"
)

// 管理后台在 identity 存储里切换注册开关后，getAppConfig 立即反映新状态，hash 随之
// 变化；清除覆盖后回到跟随环境变量的配置与 hash。接线与 cmd/telesrv 相同：
// help 读 auth.SignUpAllowed，auth 现读 identity 存储。
func TestAppConfigHashChangesAfterAdminSignUpOverride(t *testing.T) {
	ctx := context.Background()
	store := identity.NewStore(t.TempDir())
	authService := auth.NewService(nil, nil, nil, nil, nil, "12345",
		auth.WithSignUpAllowed(auth.IdentitySignUpAllowed(store, true)))
	svc := NewService(nil, nil, WithSignUpAllowed(authService.SignUpAllowed))

	open, _, err := svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := appConfigValues(t, open)["telesrv_sign_up_disabled"]; ok {
		t.Fatal("telesrv_sign_up_disabled present while env opens sign-up and no override is set")
	}

	off := false
	if err := store.SetSignUpOverride(&off); err != nil {
		t.Fatal(err)
	}
	closed, notModified, err := svc.GetAppConfig(ctx, 0, open.Hash)
	if err != nil || notModified {
		t.Fatalf("after admin override off notModified=%v err=%v, want a refreshed config", notModified, err)
	}
	if v := appConfigValues(t, closed)["telesrv_sign_up_disabled"]; v != true {
		t.Fatalf("telesrv_sign_up_disabled = %#v after admin override off, want true", v)
	}
	if closed.Hash == open.Hash {
		t.Fatalf("hash %d unchanged after admin override off", closed.Hash)
	}

	if err := store.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	cleared, notModified, err := svc.GetAppConfig(ctx, 0, closed.Hash)
	if err != nil || notModified || cleared.Hash != open.Hash {
		t.Fatalf("after clearing override hash=%d notModified=%v err=%v, want the env-open hash %d", cleared.Hash, notModified, err, open.Hash)
	}
}
