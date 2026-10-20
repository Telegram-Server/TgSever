package help

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 验证码登录关闭时下发 telesrv_code_login_disabled: true，开启时不出现这个 key；开关
// 按请求实时读取，切换后 hash 必须变化。
func TestAppConfigCodeLoginDisabledFollowsRuntimeSwitch(t *testing.T) {
	ctx := context.Background()
	var disabled atomic.Bool
	svc := NewService(nil, nil, WithCodeLoginAllowed(func(context.Context) (bool, error) {
		return !disabled.Load(), nil
	}))

	enabled, _, err := svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := appConfigValues(t, enabled)["telesrv_code_login_disabled"]; ok {
		t.Fatalf("telesrv_code_login_disabled = %#v while code login is enabled, want absent", v)
	}
	if plain, _, _ := NewService(nil, nil).GetAppConfig(ctx, 0, 0); plain.Hash != enabled.Hash {
		t.Fatalf("enabled hash %d differs from a server without the switch (%d)", enabled.Hash, plain.Hash)
	}

	disabled.Store(true)
	closed, notModified, err := svc.GetAppConfig(ctx, 0, enabled.Hash)
	if err != nil || notModified {
		t.Fatalf("after disabling code login notModified=%v err=%v, want a refreshed config", notModified, err)
	}
	if v := appConfigValues(t, closed)["telesrv_code_login_disabled"]; v != true {
		t.Fatalf("telesrv_code_login_disabled = %#v while code login is disabled, want true", v)
	}
	if closed.Hash == enabled.Hash {
		t.Fatalf("hash %d identical with code login enabled and disabled", closed.Hash)
	}
	if _, notModified, err := svc.GetAppConfig(ctx, 0, closed.Hash); err != nil || !notModified {
		t.Fatalf("same disabled state notModified=%v err=%v, want notModified", notModified, err)
	}

	disabled.Store(false)
	reopened, notModified, err := svc.GetAppConfig(ctx, 0, closed.Hash)
	if err != nil || notModified || reopened.Hash != enabled.Hash {
		t.Fatalf("after re-enabling hash=%d notModified=%v err=%v, want the enabled hash %d", reopened.Hash, notModified, err, enabled.Hash)
	}
}

// 两个开关是独立的 key：同时关闭时都下发，且三种组合的 hash 互不相同。
func TestAppConfigCodeLoginAndSignUpSwitchesAreIndependent(t *testing.T) {
	ctx := context.Background()
	var codeOff, signUpOff atomic.Bool
	svc := NewService(nil, nil,
		WithCodeLoginAllowed(func(context.Context) (bool, error) { return !codeOff.Load(), nil }),
		WithSignUpAllowed(func(context.Context) (bool, error) { return !signUpOff.Load(), nil }),
	)
	hashes := map[int]string{}
	for _, tc := range []struct {
		name          string
		code, signUp  bool
		wantCodeKey   bool
		wantSignUpKey bool
	}{
		{"both on", false, false, false, false},
		{"code off", true, false, true, false},
		{"sign-up off", false, true, false, true},
		{"both off", true, true, true, true},
	} {
		codeOff.Store(tc.code)
		signUpOff.Store(tc.signUp)
		cfg, _, err := svc.GetAppConfig(ctx, 0, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		values := appConfigValues(t, cfg)
		if _, ok := values["telesrv_code_login_disabled"]; ok != tc.wantCodeKey {
			t.Fatalf("%s: telesrv_code_login_disabled present=%v, want %v", tc.name, ok, tc.wantCodeKey)
		}
		if _, ok := values["telesrv_sign_up_disabled"]; ok != tc.wantSignUpKey {
			t.Fatalf("%s: telesrv_sign_up_disabled present=%v, want %v", tc.name, ok, tc.wantSignUpKey)
		}
		if prev, dup := hashes[cfg.Hash]; dup {
			t.Fatalf("%s: hash %d identical to %s", tc.name, cfg.Hash, prev)
		}
		hashes[cfg.Hash] = tc.name
	}
}

// 来源读取失败时按关闭下发，与 auth 的 fail-closed 拦截一致。
func TestAppConfigCodeLoginSwitchErrorReportsDisabled(t *testing.T) {
	svc := NewService(nil, nil, WithCodeLoginAllowed(func(context.Context) (bool, error) {
		return true, errors.New("identity store unreadable")
	}))
	cfg, _, err := svc.GetAppConfig(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v := appConfigValues(t, cfg)["telesrv_code_login_disabled"]; v != true {
		t.Fatalf("telesrv_code_login_disabled = %#v on source error, want true", v)
	}
}

// DB 覆盖行里的 telesrv_code_login_disabled 不能伪造服务器状态：只由开关决定。
func TestAppConfigCodeLoginDisabledIgnoresStoredOverride(t *testing.T) {
	ctx := context.Background()
	helpStore := memory.NewHelpStore()
	if err := helpStore.UpsertAppConfig(ctx, domain.AppConfig{
		Client: tdesktopClient,
		Hash:   1_000_000,
		JSON:   []byte(`{"quote_length_max":1024,"telesrv_code_login_disabled":true}`),
	}); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	cfg, _, err := NewService(helpStore, helpStore).GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := appConfigValues(t, cfg)["telesrv_code_login_disabled"]; ok {
		t.Fatalf("stored telesrv_code_login_disabled = %#v leaked while the switch is on, want absent", v)
	}
}
