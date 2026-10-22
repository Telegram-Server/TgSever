package admin

import (
	"context"
	"errors"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 后台给账号设置或清除设备上限（#105）。走普通的命令通道：试运行、按 command id 幂等、写审计。
// 上限按已绑定设备数计（#112，绑定规则见 store.DeviceLimitStore），只作用于以后的新登录：
// 调低到比已绑定数还少时不踢任何设备、不删绑定；清除上限也保留绑定名单。设上限时在同一事务里
// 把已登录、上报过 device_id 的设备按登录先后补绑到上限（#115），结果的 backfilled_devices
// 给出补绑数（试运行里是将要补绑的数量）。系统账号、bot、已注销与
// 不存在的账号一律拒绝（USER_INVALID）；取值必须在 [domain.MinDeviceLimit, domain.MaxDeviceLimit]。

const ActionSetDeviceLimit = "account.set_device_limit"

// DeviceLimitStore 是设备上限的读写面，生产里是 postgres.AuthorizationStore（绑定事务里
// 执行上限的同一个 store）。
type DeviceLimitStore = store.DeviceLimitStore

// SetDeviceLimitRequest 设置或清除 UserID 的设备上限。DeviceLimit 为 nil（JSON null 或省略）
// 表示清除，即恢复不限制。
type SetDeviceLimitRequest struct {
	CommandMeta
	UserID      int64 `json:"user_id"`
	DeviceLimit *int  `json:"device_limit"`
}

// SetDeviceLimit 处理 account.set_device_limit。非法取值在进入命令通道之前就拒绝，不留命令记录。
func (s *Service) SetDeviceLimit(ctx context.Context, req SetDeviceLimitRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.DeviceLimit != nil && !domain.ValidDeviceLimit(*req.DeviceLimit) {
		return CommandResult{}, codedError("DEVICE_LIMIT_INVALID",
			fmt.Errorf("device_limit must be between %d and %d: %w", domain.MinDeviceLimit, domain.MaxDeviceLimit, domain.ErrDeviceLimitInvalid))
	}
	if s == nil || s.deviceLimits == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin device limit dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetDeviceLimit, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		if err := s.checkDeviceLimitTarget(ctx, req.UserID); err != nil {
			return CommandResult{}, err
		}
		prev, had, err := s.deviceLimits.GetDeviceLimit(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"previous_device_limit": deviceLimitDetail(prev, had),
			"device_limit":          nil,
			"would_change":          deviceLimitWouldChange(prev, had, req.DeviceLimit),
		}
		if req.DeviceLimit != nil {
			details["device_limit"] = *req.DeviceLimit
		}
		// backfilled_devices：设上限时按登录先后补绑的设备数（#115）；试运行里是将要补绑的数量，
		// 清除上限时恒为 0。
		details["backfilled_devices"] = 0
		if req.DryRun {
			if req.DeviceLimit != nil {
				n, err := s.deviceLimits.PreviewDeviceLimitBackfill(ctx, req.UserID, *req.DeviceLimit)
				if err != nil {
					return CommandResult{Details: details}, deviceLimitStoreError(err)
				}
				details["backfilled_devices"] = n
			}
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		if req.DeviceLimit == nil {
			if _, err := s.deviceLimits.ClearDeviceLimit(ctx, req.UserID); err != nil {
				return CommandResult{Details: details}, err
			}
			return CommandResult{Message: "device limit cleared", Details: details}, nil
		}
		n, err := s.deviceLimits.SetDeviceLimit(ctx, req.UserID, *req.DeviceLimit)
		if err != nil {
			return CommandResult{Details: details}, deviceLimitStoreError(err)
		}
		details["backfilled_devices"] = n
		return CommandResult{Message: "device limit set", Details: details}, nil
	})
}

// checkDeviceLimitTarget 只接受存在、未注销的普通账号。
func (s *Service) checkDeviceLimitTarget(ctx context.Context, userID int64) error {
	invalid := codedError("USER_INVALID", fmt.Errorf("user %d cannot have a device limit", userID))
	if domain.IsSystemUserID(userID) {
		return invalid
	}
	u, found, err := s.users.AdminUser(ctx, userID)
	if err != nil {
		return err
	}
	if !found || u.Deleted || u.Bot {
		return invalid
	}
	return nil
}

func deviceLimitStoreError(err error) error {
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		return codedError("USER_INVALID", err)
	case errors.Is(err, domain.ErrDeviceLimitInvalid):
		return codedError("DEVICE_LIMIT_INVALID", err)
	default:
		return err
	}
}

func deviceLimitDetail(limit int, found bool) any {
	if !found {
		return nil
	}
	return limit
}

func deviceLimitWouldChange(prev int, had bool, next *int) bool {
	if next == nil {
		return had
	}
	return !had || prev != *next
}
