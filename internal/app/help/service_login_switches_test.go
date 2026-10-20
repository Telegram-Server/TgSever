package help

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// 生产接线：每次 getAppConfig 只调用一次快照函数（只读一次 identity），同时得出注册开关
// 与验证码登录开关两个状态 key。
func TestAppConfigReadsLoginSwitchesOncePerRequest(t *testing.T) {
	ctx := context.Background()
	calls := 0
	state := domain.LoginSwitches{SignUpAllowed: false, CodeLoginAllowed: false}
	svc := NewService(nil, nil, WithLoginSwitches(func(context.Context) domain.LoginSwitches {
		calls++
		return state
	}))

	cfg, _, err := svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("login switch snapshot called %d times for one getAppConfig, want 1", calls)
	}
	values := appConfigValues(t, cfg)
	if values["telesrv_sign_up_disabled"] != true || values["telesrv_code_login_disabled"] != true {
		t.Fatalf("both switches off: values=%v, want both keys", values)
	}

	state = domain.LoginSwitches{SignUpAllowed: true, CodeLoginAllowed: true}
	cfg, _, err = svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	values = appConfigValues(t, cfg)
	if _, ok := values["telesrv_sign_up_disabled"]; ok {
		t.Fatalf("sign-up open: telesrv_sign_up_disabled present")
	}
	if _, ok := values["telesrv_code_login_disabled"]; ok {
		t.Fatalf("code login on: telesrv_code_login_disabled present")
	}
}

// identity.json 读不出时：密码登录开启则两个开关都按关闭下发；密码登录关闭则验证码登录
// 照常开启（不变量），只有注册按关闭下发。
func TestAppConfigLoginSwitchesUnreadableIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := identity.NewStore(dir)
	for _, tc := range []struct {
		passwordLogin bool
		wantCodeKey   bool
	}{
		{true, true},
		{false, false},
	} {
		svc := NewService(nil, nil, WithLoginSwitches(auth.IdentityLoginSwitches(store, true, true, tc.passwordLogin)))
		cfg, _, err := svc.GetAppConfig(context.Background(), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		values := appConfigValues(t, cfg)
		if values["telesrv_sign_up_disabled"] != true {
			t.Fatalf("password login=%v: telesrv_sign_up_disabled = %#v, want true", tc.passwordLogin, values["telesrv_sign_up_disabled"])
		}
		if _, ok := values["telesrv_code_login_disabled"]; ok != tc.wantCodeKey {
			t.Fatalf("password login=%v: telesrv_code_login_disabled present=%v, want %v", tc.passwordLogin, ok, tc.wantCodeKey)
		}
	}
}
