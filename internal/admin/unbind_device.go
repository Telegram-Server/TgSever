package admin

import (
	"context"
	"errors"
	"fmt"

	"telesrv/internal/domain"
)

// 后台解绑一台设备（#114）。走普通的命令通道：试运行、按 command id 幂等与冲突检测、写审计。
// 删除账号对 device_id 的绑定（释放名额），并撤销这个账号在 device_id 相同的 auth key 上的
// 全部授权；撤销方式与远程踢设备（account.revoke_sessions）相同：store 只删业务授权、保留
// 协议 key，再关闭这些 key 上的在线连接，客户端重连后收到 401 并退出。删除绑定与撤销授权
// 在 store 的同一个事务里完成，命令失败时两者都没有发生，可以安全重试。
// 系统账号、bot、已注销与不存在的账号返回 USER_INVALID；绑定不存在返回 DEVICE_BINDING_NOT_FOUND。

const ActionUnbindDevice = "account.unbind_device"

// UnbindDeviceRequest 解绑 UserID 的设备 DeviceID（绑定名单里的 device_id）。
type UnbindDeviceRequest struct {
	CommandMeta
	UserID   int64  `json:"user_id"`
	DeviceID string `json:"device_id"`
}

// UnbindDevice 处理 account.unbind_device。缺少 user_id / device_id 时在进入命令通道之前拒绝。
func (s *Service) UnbindDevice(ctx context.Context, req UnbindDeviceRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.DeviceID == "" {
		return CommandResult{}, fmt.Errorf("device_id is required")
	}
	if s == nil || s.deviceLimits == nil || s.users == nil || s.revoker == nil {
		return CommandResult{}, fmt.Errorf("admin device binding dependencies are not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionUnbindDevice, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		if err := s.checkDeviceLimitTarget(ctx, req.UserID); err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{"device_id": req.DeviceID}
		if req.DryRun {
			// revoked_count：试运行里是将要撤销（下线）的授权数。
			n, err := s.deviceLimits.PreviewUnbindDevice(ctx, req.UserID, req.DeviceID)
			if err != nil {
				return CommandResult{Details: details}, unbindDeviceStoreError(err)
			}
			details["revoked_count"] = n
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		revoked, err := s.deviceLimits.UnbindDevice(ctx, req.UserID, req.DeviceID)
		if err != nil {
			return CommandResult{Details: details}, unbindDeviceStoreError(err)
		}
		details["revoked_hashes"] = authorizationHashes(revoked)
		details["revoked_count"] = len(revoked)
		if err := s.revokeAuthKeys(ctx, req.UserID, revoked); err != nil {
			return CommandResult{Details: details}, err
		}
		return CommandResult{Message: "device unbound", Details: details}, nil
	})
}

func unbindDeviceStoreError(err error) error {
	if errors.Is(err, domain.ErrDeviceBindingNotFound) {
		return codedError("DEVICE_BINDING_NOT_FOUND", err)
	}
	return err
}
