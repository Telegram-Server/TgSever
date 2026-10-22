package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/admin"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
)

// TestAdminUnbindDeviceEndToEnd 只验证接线（#114）：账号上限 1、旧设备已绑定且在线时，
//   - 新设备登录收到 AUTHORIZATIONS_TOO_MUCH；
//   - 后台解绑旧设备（进程内接上服务端的授权 store 与 RPC 路由，生产里经 admin API 调同一个
//     方法）之后，旧设备的在线连接被关闭，用原来的 auth key 重连后收到 401 AUTH_KEY_UNREGISTERED；
//   - 新设备能登录，名单里只剩它。
//
// 解绑的行为矩阵在 storetest 与 internal/admin 里。
func TestAdminUnbindDeviceEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	const phone = "15550105401"
	u, err := srv.users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Staff"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	adminSvc := admin.NewService(admin.Dependencies{
		Commands:     &e2eAdminCommands{},
		Users:        users.NewService(srv.users),
		DeviceLimits: srv.authorizations,
		Revoker:      srv.router,
	})
	limit := 1
	if res, err := adminSvc.SetDeviceLimit(context.Background(), admin.SetDeviceLimitRequest{
		CommandMeta: admin.CommandMeta{CommandID: "e2e-unbind-set-limit", Actor: "ops", Reason: "staff account"},
		UserID:      u.ID,
		DeviceLimit: &limit,
	}); err != nil {
		t.Fatalf("admin SetDeviceLimit: result=%+v err=%v", res, err)
	}

	signIn := func(ctx context.Context, raw *tg.Client) error {
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+" + phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return fmt.Errorf("auth.sendCode: %w", err)
		}
		_, err = raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "+" + phone, PhoneCodeHash: sent.(*tg.AuthSentCode).PhoneCodeHash, PhoneCode: loginPasswordTestCode})
		return err
	}

	// 旧设备登录后保持在线，等后台解绑之后再发一次业务调用。
	oldStorage := &session.StorageMemory{}
	online := make(chan struct{})
	unbound := make(chan struct{})
	oldDevice := make(chan error, 1)
	go func() {
		oldDevice <- srv.runOnDevice(t, oldStorage, e2eDevice("staff-old-0001"), func(ctx context.Context, raw *tg.Client) error {
			if err := signIn(ctx, raw); err != nil {
				return fmt.Errorf("old device auth.signIn: %w", err)
			}
			if _, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{}); err != nil {
				return fmt.Errorf("old device before unbinding: %w", err)
			}
			close(online)
			select {
			case <-unbound:
			case <-ctx.Done():
				return ctx.Err()
			}
			// 解绑关闭了这台设备的连接：这一次调用要么已经拿到 401，要么断在连接上；
			// 重连之后的 401 在下面单独验证。
			_, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err == nil {
				return fmt.Errorf("old device after unbinding: business call succeeded")
			}
			var rpcErr *tgerr.Error
			if errors.As(err, &rpcErr) {
				return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
			}
			return nil
		})
	}()
	select {
	case <-online:
	case err := <-oldDevice:
		t.Fatalf("old device: %v", err)
	}

	newStorage := &session.StorageMemory{}
	newDevice := e2eDevice("staff-new-0002")
	if err := srv.runOnDevice(t, newStorage, newDevice, func(ctx context.Context, raw *tg.Client) error {
		if err := signIn(ctx, raw); !tgerr.Is(err, "AUTHORIZATIONS_TOO_MUCH") {
			return fmt.Errorf("new device before unbinding err=%v, want AUTHORIZATIONS_TOO_MUCH", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	res, err := adminSvc.UnbindDevice(context.Background(), admin.UnbindDeviceRequest{
		CommandMeta: admin.CommandMeta{CommandID: "e2e-unbind-device", Actor: "ops", Reason: "replaced laptop"},
		UserID:      u.ID,
		DeviceID:    "staff-old-0001",
	})
	if err != nil || res.Details["revoked_count"] != 1 {
		t.Fatalf("admin UnbindDevice: result=%+v err=%v", res, err)
	}
	close(unbound)
	if err := <-oldDevice; err != nil {
		t.Fatalf("old device: %v", err)
	}
	// 协议 key 保留：旧设备用原来的 auth key 重连，收到 401 后客户端会退出登录。
	if err := srv.runOnDevice(t, oldStorage, e2eDevice(""), func(ctx context.Context, raw *tg.Client) error {
		_, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
	}); err != nil {
		t.Fatalf("old device reconnecting after unbinding: %v", err)
	}

	if err := srv.runOnDevice(t, newStorage, newDevice, func(ctx context.Context, raw *tg.Client) error {
		if err := signIn(ctx, raw); err != nil {
			return fmt.Errorf("new device after unbinding: %w", err)
		}
		_, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	bindings, err := srv.authorizations.ListDeviceBindings(context.Background(), u.ID)
	if err != nil || len(bindings) != 1 || bindings[0].DeviceID != "staff-new-0002" {
		t.Fatalf("device bindings=%+v err=%v, want only staff-new-0002", bindings, err)
	}
}
