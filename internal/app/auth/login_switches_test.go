package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// IdentityLoginSwitches 一次读取 identity 同时得出注册开关与验证码登录开关，供
// help.getAppConfig 下发；结果与两个单独的判断函数一致，读失败时两者都按关闭，
// 但遵守"密码登录关闭时验证码登录一定开启"的不变量。

func TestIdentityLoginSwitchesResolvesBothSwitchesFromOneRead(t *testing.T) {
	store := identity.NewStore(t.TempDir())
	if err := store.SetSignUpOverride(signUpOverride(false)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCodeLoginOverride(signUpOverride(false)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got := IdentityLoginSwitches(store, true, true, true, false)(ctx); got != (domain.LoginSwitches{}) {
		t.Fatalf("both overrides off, password login on: %+v, want both off", got)
	}
	if got := IdentityLoginSwitches(store, true, true, false, false)(ctx); got != (domain.LoginSwitches{CodeLoginAllowed: true}) {
		t.Fatalf("both overrides off, password login off: %+v, want only code login on", got)
	}
	if err := store.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	want := domain.LoginSwitches{SignUpAllowed: true, CodeLoginAllowed: false}
	if got := IdentityLoginSwitches(store, true, false, true, false)(ctx); got != want {
		t.Fatalf("no overrides: %+v, want env defaults %+v", got, want)
	}
}

// 游客登录开关：覆盖值优先，未设置时跟随环境变量；与 IdentityGuestLogin 判断一致。
func TestIdentityLoginSwitchesGuestLogin(t *testing.T) {
	store := identity.NewStore(t.TempDir())
	ctx := context.Background()
	check := func(env bool, want bool) {
		t.Helper()
		if got := IdentityLoginSwitches(store, true, true, true, env)(ctx).GuestLoginAllowed; got != want {
			t.Fatalf("env=%v: GuestLoginAllowed=%v, want %v", env, got, want)
		}
		settings, err := IdentityGuestLogin(store, env, 7)(ctx)
		if err != nil || settings.Enabled != want {
			t.Fatalf("env=%v: IdentityGuestLogin=%+v err=%v, want enabled=%v", env, settings, err, want)
		}
	}
	check(false, false)
	check(true, true)
	on, off, days := true, false, 30
	if err := store.SetGuestLoginOverride(&on, &days); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	if settings, _ := IdentityGuestLogin(store, false, 7)(ctx); settings.IPBindDays != 30 {
		t.Fatalf("days override = %d, want 30", settings.IPBindDays)
	}
	if err := store.SetGuestLoginOverride(&off, nil); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	if settings, _ := IdentityGuestLogin(store, true, 9)(ctx); settings.IPBindDays != 9 {
		t.Fatalf("days after clearing override = %d, want env 9", settings.IPBindDays)
	}
}

func TestIdentityLoginSwitchesUnreadableIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := identity.NewStore(dir)
	ctx := context.Background()
	// 游客登录的环境默认值是开启，读失败时也按关闭。
	if got := IdentityLoginSwitches(store, true, true, true, true)(ctx); got != (domain.LoginSwitches{}) {
		t.Fatalf("unreadable identity, password login on: %+v, want all off", got)
	}
	if got := IdentityLoginSwitches(store, true, true, false, true)(ctx); got != (domain.LoginSwitches{CodeLoginAllowed: true}) {
		t.Fatalf("unreadable identity, password login off: %+v, want sign-up off, code login on", got)
	}
	if _, err := IdentityGuestLogin(store, true, 7)(ctx); err == nil {
		t.Fatal("IdentityGuestLogin on unreadable identity returned no error")
	}
}
