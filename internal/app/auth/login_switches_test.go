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
	if got := IdentityLoginSwitches(store, true, true, true)(ctx); got != (domain.LoginSwitches{}) {
		t.Fatalf("both overrides off, password login on: %+v, want both off", got)
	}
	if got := IdentityLoginSwitches(store, true, true, false)(ctx); got != (domain.LoginSwitches{CodeLoginAllowed: true}) {
		t.Fatalf("both overrides off, password login off: %+v, want only code login on", got)
	}
	if err := store.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	want := domain.LoginSwitches{SignUpAllowed: true, CodeLoginAllowed: false}
	if got := IdentityLoginSwitches(store, true, false, true)(ctx); got != want {
		t.Fatalf("no overrides: %+v, want env defaults %+v", got, want)
	}
}

func TestIdentityLoginSwitchesUnreadableIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := identity.NewStore(dir)
	ctx := context.Background()
	if got := IdentityLoginSwitches(store, true, true, true)(ctx); got != (domain.LoginSwitches{}) {
		t.Fatalf("unreadable identity, password login on: %+v, want both off", got)
	}
	if got := IdentityLoginSwitches(store, true, true, false)(ctx); got != (domain.LoginSwitches{CodeLoginAllowed: true}) {
		t.Fatalf("unreadable identity, password login off: %+v, want sign-up off, code login on", got)
	}
}
