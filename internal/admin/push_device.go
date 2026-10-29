package admin

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	"telesrv/internal/domain"
)

// ActionSetPushDeviceEnabled 是后台推送开关：打开或关闭一个会话（授权）的离线推送。关闭后
// 推送服务不向它的任何登记（APNs 与 VoIP）发送推送，打开则与平常一样。后台路由由 push.manage
// 把关；走普通的命令通道：试运行、按 command id 幂等、写审计。
const ActionSetPushDeviceEnabled = "push.set_device_enabled"

// CodePushDeviceNotFound：该账号的这个会话下没有推送登记（没登记过、已退出，或会话不属于该账号）。
const CodePushDeviceNotFound = "PUSH_DEVICE_NOT_FOUND"

// PushDeviceStore 是推送开关命令用到的设备登记存储（store.PushDeviceStore 的子集）。
type PushDeviceStore interface {
	ListPushDevicesByUser(ctx context.Context, userID int64) ([]domain.PushDevice, error)
	SetPushDevicesEnabled(ctx context.Context, userID int64, authKeyID [8]byte, enabled bool) (int, error)
}

// SetPushDeviceEnabledRequest 打开或关闭 UserID 的会话 AuthKeyID 的推送。AuthKeyID 是账号详情
// 设备列表里的 auth_key_id（authorizations 主键）；JSON 里按十进制字符串传，避免前端丢精度。
type SetPushDeviceEnabledRequest struct {
	CommandMeta
	UserID    int64 `json:"user_id"`
	AuthKeyID int64 `json:"auth_key_id,string"`
	Enabled   bool  `json:"enabled"`
}

// SetPushDeviceEnabled 处理 push.set_device_enabled：该会话下属于 UserID 的全部登记一起开关。
// 会话下没有该账号的登记时返回 PUSH_DEVICE_NOT_FOUND（试运行同样校验）。
func (s *Service) SetPushDeviceEnabled(ctx context.Context, req SetPushDeviceEnabledRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.AuthKeyID == 0 {
		return CommandResult{}, fmt.Errorf("auth_key_id is required")
	}
	if s == nil || s.pushDevices == nil {
		return CommandResult{}, fmt.Errorf("admin push device dependency is not configured")
	}
	authKeyID := authKeyIDFromInt64(req.AuthKeyID)
	return s.runCommand(ctx, req.CommandMeta, ActionSetPushDeviceEnabled, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"auth_key_id": strconv.FormatInt(req.AuthKeyID, 10),
			"enabled":     req.Enabled,
		}
		devices, err := s.pushDevices.ListPushDevicesByUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		count := 0
		for _, device := range devices {
			if device.AuthKeyID == authKeyID {
				count++
			}
		}
		if count == 0 {
			return CommandResult{Details: details}, codedError(CodePushDeviceNotFound, fmt.Errorf("no push registration of user %d on this session", req.UserID))
		}
		if req.DryRun {
			details["registration_count"] = count
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.pushDevices.SetPushDevicesEnabled(ctx, req.UserID, authKeyID, req.Enabled)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if updated == 0 {
			// 读到登记之后会话被删除（退出登录、吊销）。
			return CommandResult{Details: details}, codedError(CodePushDeviceNotFound, fmt.Errorf("push registration of user %d is gone", req.UserID))
		}
		details["updated_count"] = updated
		if req.Enabled {
			return CommandResult{Message: "push enabled", Details: details}, nil
		}
		return CommandResult{Message: "push disabled", Details: details}, nil
	})
}

// authKeyIDFromInt64 把 authorizations 主键（小端 int64，与存储一致）还原为 auth key id。
func authKeyIDFromInt64(v int64) [8]byte {
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], uint64(v))
	return id
}
