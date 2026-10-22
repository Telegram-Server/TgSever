package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/admin"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
)

// TestAdminDeviceLimitEndToEnd 只验证接线（#105、#112）：经后台命令（进程内接上服务端的授权
// store，生产里经 admin API 调同一个方法）给账号设上限 1 之后：
//   - 第一台设备先只连一次上报 device_id，重连时 initConnection 不再带它，验证码登录仍成功
//     （登录读的是 auth key 上持久化的 device_id），这台设备被绑定；
//   - 没上报 device_id 的设备登录收到 DEVICE_ID_REQUIRED；
//   - 另一台新设备登录收到 AUTHORIZATIONS_TOO_MUCH；
//   - 已绑定的设备退出登录后，重装（新 auth key、同一个 device_id）能重新登录。
//
// 绑定规则的行为矩阵在 storetest 与 internal/app/auth 里。
func TestAdminDeviceLimitEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	const phone = "15550105301"
	u, err := srv.users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Staff"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	adminSvc := admin.NewService(admin.Dependencies{
		Commands:     &e2eAdminCommands{},
		Users:        users.NewService(srv.users),
		DeviceLimits: srv.authorizations,
	})
	limit := 1
	res, err := adminSvc.SetDeviceLimit(context.Background(), admin.SetDeviceLimitRequest{
		CommandMeta: admin.CommandMeta{CommandID: "e2e-set-device-limit", Actor: "ops", Reason: "staff account"},
		UserID:      u.ID,
		DeviceLimit: &limit,
	})
	if err != nil {
		t.Fatalf("admin SetDeviceLimit: result=%+v err=%v", res, err)
	}

	signIn := func(ctx context.Context, raw *tg.Client) (tg.AuthAuthorizationClass, error) {
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+" + phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return nil, fmt.Errorf("auth.sendCode: %w", err)
		}
		return raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "+" + phone, PhoneCodeHash: sent.(*tg.AuthSentCode).PhoneCodeHash, PhoneCode: loginPasswordTestCode})
	}
	signInExpecting := func(want string) func(ctx context.Context, raw *tg.Client) error {
		return func(ctx context.Context, raw *tg.Client) error {
			_, err := signIn(ctx, raw)
			if !tgerr.Is(err, want) {
				return fmt.Errorf("auth.signIn err=%v, want %s", err, want)
			}
			return nil
		}
	}
	signInOK := func(ctx context.Context, raw *tg.Client) error {
		if _, err := signIn(ctx, raw); err != nil {
			return fmt.Errorf("auth.signIn: %w", err)
		}
		return nil
	}
	run := func(what string, storage session.Storage, device telegram.DeviceConfig, fn func(ctx context.Context, raw *tg.Client) error) {
		t.Helper()
		if err := srv.runOnDevice(t, storage, device, fn); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	bound := e2eDevice("staff-desk-0001")
	boundStorage := &session.StorageMemory{}
	srv.runWithDevice(t, boundStorage, bound)
	run("bound device reconnects without device_id and signs in", boundStorage, e2eDevice(""), signInOK)

	run("device without device_id", &session.StorageMemory{}, e2eDevice(""), signInExpecting("DEVICE_ID_REQUIRED"))
	run("new device at the limit", &session.StorageMemory{}, e2eDevice("staff-desk-0002"), signInExpecting("AUTHORIZATIONS_TOO_MUCH"))

	run("bound device logs out", boundStorage, e2eDevice(""), func(ctx context.Context, raw *tg.Client) error {
		_, err := raw.AuthLogOut(ctx)
		return err
	})
	run("bound device signs in again after reinstalling", &session.StorageMemory{}, bound, signInOK)

	bindings, err := srv.authorizations.ListDeviceBindings(context.Background(), u.ID)
	if err != nil || len(bindings) != 1 || bindings[0].DeviceID != "staff-desk-0001" {
		t.Fatalf("device bindings=%+v err=%v, want only staff-desk-0001", bindings, err)
	}
}

// e2eDevice 是一台桌面客户端的 initConnection 设备信息；deviceID 为空时 params 里不带 device_id。
func e2eDevice(deviceID string) telegram.DeviceConfig {
	params := []tg.JSONObjectValue{{Key: "tz_offset", Value: &tg.JSONNumber{Value: 0}}}
	if deviceID != "" {
		params = append(params, tg.JSONObjectValue{Key: "device_id", Value: &tg.JSONString{Value: deviceID}})
	}
	return telegram.DeviceConfig{
		DeviceModel: "Desktop", SystemVersion: "Windows 11", AppVersion: "6.0.0 x64",
		SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
		Params: &tg.JSONObject{Value: params},
	}
}
