package mtprotoedge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/admin"
	"telesrv/internal/app/users"
)

// TestAdminPermissionsEndToEnd 只验证接线（#119）：在线会话里的员工账号，经后台命令（进程内接上
// 服务端的权限模块与 Router，生产里经 admin API 调同一个方法）打开总开关并关掉"编辑消息"后：
//   - 会话收到 updateConfig，重新拉取的 appConfig 里有 telesrv_permissions 且 edit=false；
//   - messages.editMessage 返回 403 TELESRV_PERMISSION_EDIT；
//
// 关闭总开关后再次收到 updateConfig，appConfig 里不再有这个 key，编辑恢复正常。
// 判定矩阵在 internal/app/permissions，appConfig 形状在 internal/app/help。
func TestAdminPermissionsEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	adminSvc := admin.NewService(admin.Dependencies{
		Commands:            &e2eAdminCommands{},
		Users:               users.NewService(srv.users),
		Permissions:         srv.permissions,
		PermissionsNotifier: srv.router,
	})
	setPermissions := func(commandID string, req admin.SetPermissionsRequest) error {
		req.CommandMeta = admin.CommandMeta{CommandID: commandID, Actor: "ops", Reason: "staff account"}
		res, err := adminSvc.SetPermissions(context.Background(), req)
		if err != nil {
			return fmt.Errorf("admin SetPermissions: result=%+v err=%w", res, err)
		}
		if res.Details["notify_error"] != nil {
			return fmt.Errorf("admin SetPermissions notify_error=%v", res.Details["notify_error"])
		}
		return nil
	}

	configUpdates := make(chan struct{}, 8)
	handler := telegram.UpdateHandlerFunc(func(_ context.Context, u tg.UpdatesClass) error {
		for _, upd := range updatesFromClass(u) {
			if _, ok := upd.(*tg.UpdateConfig); ok {
				configUpdates <- struct{}{}
			}
		}
		return nil
	})
	waitConfig := func(ctx context.Context) error {
		select {
		case <-configUpdates:
			return nil
		case <-time.After(10 * time.Second):
			return fmt.Errorf("no updateConfig within 10s")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	flag := func(v bool) *bool { return &v }

	err := srv.runWithUpdates(t, handler, func(ctx context.Context, raw *tg.Client) error {
		self, err := signUpWithCode(ctx, raw, "+15550119001", "Staff")
		if err != nil {
			return fmt.Errorf("sign up: %w", err)
		}
		// 裸 RPC 置 receivesUpdates，推送才会到达这个会话。
		if _, err := raw.UpdatesGetState(ctx); err != nil {
			return err
		}
		sent, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "note", RandomID: 1})
		if err != nil {
			return fmt.Errorf("send note: %w", err)
		}
		msgID, err := sentMessageID(sent)
		if err != nil {
			return err
		}
		if perms, err := appConfigPermissions(ctx, raw); err != nil || perms != nil {
			return fmt.Errorf("telesrv_permissions before setting = %v err=%v, want absent", perms, err)
		}

		if err := setPermissions("e2e-permissions-on", admin.SetPermissionsRequest{UserID: self.ID, PermissionsRequest: admin.PermissionsRequest{Enabled: flag(true), Edit: flag(false)}}); err != nil {
			return err
		}
		if err := waitConfig(ctx); err != nil {
			return fmt.Errorf("after switching on: %w", err)
		}
		perms, err := appConfigPermissions(ctx, raw)
		if err != nil {
			return err
		}
		if perms == nil || perms["edit"] != false || perms["send"] != true {
			return fmt.Errorf("telesrv_permissions = %v, want edit=false send=true", perms)
		}
		_, err = raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerSelf{}, ID: msgID, Message: "edited"})
		if rpcErr, ok := tgerr.As(err); !ok || rpcErr.Code != 403 || rpcErr.Type != "TELESRV_PERMISSION_EDIT" {
			return fmt.Errorf("editMessage err=%v, want 403 TELESRV_PERMISSION_EDIT", err)
		}

		if err := setPermissions("e2e-permissions-off", admin.SetPermissionsRequest{UserID: self.ID, PermissionsRequest: admin.PermissionsRequest{Enabled: flag(false)}}); err != nil {
			return err
		}
		if err := waitConfig(ctx); err != nil {
			return fmt.Errorf("after switching off: %w", err)
		}
		if perms, err := appConfigPermissions(ctx, raw); err != nil || perms != nil {
			return fmt.Errorf("telesrv_permissions after switching off = %v err=%v, want absent", perms, err)
		}
		if _, err := raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerSelf{}, ID: msgID, Message: "edited"}); err != nil {
			return fmt.Errorf("editMessage after switching off: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// appConfigPermissions 返回 appConfig 里的 telesrv_permissions；不存在时返回 nil。
func appConfigPermissions(ctx context.Context, raw *tg.Client) (map[string]any, error) {
	res, err := raw.HelpGetAppConfig(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("help.getAppConfig: %w", err)
	}
	cfg, ok := res.(*tg.HelpAppConfig)
	if !ok {
		return nil, fmt.Errorf("help.getAppConfig = %T, want *tg.HelpAppConfig", res)
	}
	object, ok := cfg.Config.(*tg.JSONObject)
	if !ok {
		return nil, fmt.Errorf("app config = %T, want *tg.JSONObject", cfg.Config)
	}
	for _, item := range object.Value {
		if item.Key != "telesrv_permissions" {
			continue
		}
		value, ok := item.Value.(*tg.JSONObject)
		if !ok {
			return nil, fmt.Errorf("telesrv_permissions = %T, want object", item.Value)
		}
		out := make(map[string]any, len(value.Value))
		for _, field := range value.Value {
			switch v := field.Value.(type) {
			case *tg.JSONBool:
				out[field.Key] = v.Value
			default:
				encoded, _ := json.Marshal(v)
				out[field.Key] = string(encoded)
			}
		}
		return out, nil
	}
	return nil, nil
}

func sentMessageID(u tg.UpdatesClass) (int, error) {
	for _, upd := range updatesFromClass(u) {
		switch v := upd.(type) {
		case *tg.UpdateMessageID:
			return v.ID, nil
		case *tg.UpdateNewMessage:
			return v.Message.GetID(), nil
		}
	}
	if short, ok := u.(*tg.UpdateShortSentMessage); ok {
		return short.ID, nil
	}
	return 0, fmt.Errorf("sendMessage result %T carries no message id", u)
}
