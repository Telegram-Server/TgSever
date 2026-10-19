package help

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 密码登录总开关打开时下发 telesrv_features，关闭时不出现这个 key；hash 必须跟着变化，
// 否则持有旧 hash 的客户端只会收到 notModified，永远看不到入口。
func TestAppConfigTelesrvFeaturesFollowPasswordLoginSwitch(t *testing.T) {
	on := walletConfigValues(t, NewService(nil, nil, WithPasswordLogin(true)))
	features, ok := on["telesrv_features"].([]any)
	if !ok || len(features) != 1 || features[0] != "password_login" {
		t.Fatalf("telesrv_features = %#v, want [password_login]", on["telesrv_features"])
	}
	off := walletConfigValues(t, NewService(nil, nil, WithPasswordLogin(false)))
	if _, ok := off["telesrv_features"]; ok {
		t.Fatalf("telesrv_features = %#v with the switch off, want absent", off["telesrv_features"])
	}

	ctx := context.Background()
	offCfg, _, err := NewService(nil, nil, WithPasswordLogin(false)).GetAppConfig(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	onCfg, notModified, err := NewService(nil, nil, WithPasswordLogin(true)).GetAppConfig(ctx, 0, offCfg.Hash)
	if err != nil || notModified {
		t.Fatalf("client holding the switch-off hash got notModified=%v err=%v, want a refreshed config", notModified, err)
	}
	if onCfg.Hash == offCfg.Hash {
		t.Fatalf("hash %d is identical with password login on and off", onCfg.Hash)
	}
	// 同一个开关状态下 hash 稳定，客户端带上它会得到 notModified。
	if _, notModified, err := NewService(nil, nil, WithPasswordLogin(true)).GetAppConfig(ctx, 0, onCfg.Hash); err != nil || !notModified {
		t.Fatalf("same switch state notModified=%v err=%v, want notModified", notModified, err)
	}
}

// DB 覆盖行里的 telesrv_features 不能绕过开关：开关关闭时剔除，打开时以服务端为准。
func TestAppConfigTelesrvFeaturesIgnoreStoredOverride(t *testing.T) {
	ctx := context.Background()
	helpStore := memory.NewHelpStore()
	if err := helpStore.UpsertAppConfig(ctx, domain.AppConfig{
		Client: tdesktopClient,
		Hash:   1_000_000,
		JSON:   []byte(`{"quote_length_max":1024,"telesrv_features":["password_login","something_else"]}`),
	}); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	off := walletConfigValues(t, NewService(helpStore, helpStore, WithPasswordLogin(false)))
	if _, ok := off["telesrv_features"]; ok {
		t.Fatalf("telesrv_features = %#v from the stored row with the switch off, want absent", off["telesrv_features"])
	}
	on := walletConfigValues(t, NewService(helpStore, helpStore, WithPasswordLogin(true)))
	if features, ok := on["telesrv_features"].([]any); !ok || len(features) != 1 || features[0] != "password_login" {
		t.Fatalf("telesrv_features = %#v, want exactly [password_login]", on["telesrv_features"])
	}
}
