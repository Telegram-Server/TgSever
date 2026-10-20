package help

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func appConfigValues(t *testing.T, cfg domain.AppConfig) map[string]any {
	t.Helper()
	values := map[string]any{}
	if err := json.Unmarshal(cfg.JSON, &values); err != nil {
		t.Fatalf("decode app config: %v", err)
	}
	return values
}

// 注册关闭时下发 telesrv_sign_up_disabled: true，开放时不出现这个 key；开关是按请求
// 实时读取的，切换后 hash 必须变化，持有旧 hash 的客户端拿到新配置而不是 notModified。
func TestAppConfigSignUpDisabledFollowsRuntimeSwitch(t *testing.T) {
	ctx := context.Background()
	var disabled atomic.Bool
	svc := NewService(nil, nil, WithSignUpAllowed(func(context.Context) (bool, error) {
		return !disabled.Load(), nil
	}))

	open, _, err := svc.GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := appConfigValues(t, open)["telesrv_sign_up_disabled"]; ok {
		t.Fatalf("telesrv_sign_up_disabled = %#v while sign-up is open, want absent", v)
	}
	if plain, _, _ := NewService(nil, nil).GetAppConfig(ctx, 0, 0); plain.Hash != open.Hash {
		t.Fatalf("open hash %d differs from a server without the switch (%d): existing caches would refetch", open.Hash, plain.Hash)
	}

	disabled.Store(true)
	closed, notModified, err := svc.GetAppConfig(ctx, 0, open.Hash)
	if err != nil || notModified {
		t.Fatalf("after closing sign-up notModified=%v err=%v, want a refreshed config", notModified, err)
	}
	if v := appConfigValues(t, closed)["telesrv_sign_up_disabled"]; v != true {
		t.Fatalf("telesrv_sign_up_disabled = %#v while sign-up is closed, want true", v)
	}
	if closed.Hash == open.Hash {
		t.Fatalf("hash %d identical with sign-up open and closed", closed.Hash)
	}
	if _, notModified, err := svc.GetAppConfig(ctx, 0, closed.Hash); err != nil || !notModified {
		t.Fatalf("same closed state notModified=%v err=%v, want notModified", notModified, err)
	}

	disabled.Store(false)
	reopened, notModified, err := svc.GetAppConfig(ctx, 0, closed.Hash)
	if err != nil || notModified || reopened.Hash != open.Hash {
		t.Fatalf("after reopening hash=%d notModified=%v err=%v, want the open hash %d", reopened.Hash, notModified, err, open.Hash)
	}
}

// DB 覆盖行里的 telesrv_sign_up_disabled 不能伪造服务器状态：只由开关决定。
func TestAppConfigSignUpDisabledIgnoresStoredOverride(t *testing.T) {
	ctx := context.Background()
	helpStore := memory.NewHelpStore()
	if err := helpStore.UpsertAppConfig(ctx, domain.AppConfig{
		Client: tdesktopClient,
		Hash:   1_000_000,
		JSON:   []byte(`{"quote_length_max":1024,"telesrv_sign_up_disabled":true}`),
	}); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	cfg, _, err := NewService(helpStore, helpStore).GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := appConfigValues(t, cfg)["telesrv_sign_up_disabled"]; ok {
		t.Fatalf("telesrv_sign_up_disabled = %#v from the stored row with sign-up open, want absent", v)
	}
}

// 开关来源读取失败时按关闭下发，与 auth 服务的 fail-closed 拦截一致。
func TestAppConfigSignUpSwitchErrorReportsDisabled(t *testing.T) {
	svc := NewService(nil, nil, WithSignUpAllowed(func(context.Context) (bool, error) {
		return true, errors.New("identity store unreadable")
	}))
	cfg, _, err := svc.GetAppConfig(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v := appConfigValues(t, cfg)["telesrv_sign_up_disabled"]; v != true {
		t.Fatalf("telesrv_sign_up_disabled = %#v when the switch is unreadable, want true", v)
	}
}
