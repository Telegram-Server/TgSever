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
		svc := NewService(nil, nil, WithLoginSwitches(auth.IdentityLoginSwitches(store, true, true, tc.passwordLogin, true)))
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
		// 游客登录的环境默认值是开启，读失败时按关闭：不下发。
		if _, ok := values["telesrv_guest_login_enabled"]; ok {
			t.Fatalf("password login=%v: telesrv_guest_login_enabled present on unreadable identity", tc.passwordLogin)
		}
	}
}

// 游客登录开关：开启时才下发 telesrv_guest_login_enabled，切换会换 hash。
func TestAppConfigGuestLoginKey(t *testing.T) {
	ctx := context.Background()
	state := domain.LoginSwitches{SignUpAllowed: true, CodeLoginAllowed: true}
	svc := NewService(nil, nil, WithLoginSwitches(func(context.Context) domain.LoginSwitches { return state }))
	off, _, err := svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := appConfigValues(t, off)["telesrv_guest_login_enabled"]; ok {
		t.Fatal("guest login off: key present")
	}
	state.GuestLoginAllowed = true
	on, notModified, err := svc.GetAppConfig(ctx, 0, off.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if notModified || on.Hash == off.Hash {
		t.Fatalf("turning guest login on kept hash %d (notModified=%v)", on.Hash, notModified)
	}
	if appConfigValues(t, on)["telesrv_guest_login_enabled"] != true {
		t.Fatal("guest login on: telesrv_guest_login_enabled != true")
	}
	// 没有快照函数时按单独来源读；未注入来源视为关闭。
	plain, _, err := NewService(nil, nil).GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := appConfigValues(t, plain)["telesrv_guest_login_enabled"]; ok {
		t.Fatal("no guest source: key present")
	}
	withSource, _, err := NewService(nil, nil, WithGuestLoginAllowed(func(context.Context) (bool, error) { return true, nil })).GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if appConfigValues(t, withSource)["telesrv_guest_login_enabled"] != true {
		t.Fatal("guest source on: key missing")
	}
}
