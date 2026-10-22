package admin

import (
	"context"
	"fmt"
	"time"
)

// 后台查看账号的设备绑定名单（#113）。只读，不走命令通道；名单本身由登录时的绑定规则维护
// （#112，见 store.DeviceLimitStore），这里只负责给后台展示。

// DeviceBinding 是名单里的一台设备：DeviceID 是客户端在 initConnection.params 里上报的标识，
// 型号/平台/客户端版本记的是绑定那次登录时的值。
type DeviceBinding struct {
	DeviceID    string    `json:"device_id"`
	DeviceModel string    `json:"device_model"`
	Platform    string    `json:"platform"`
	AppVersion  string    `json:"app_version"`
	BoundAt     time.Time `json:"bound_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
}

// DeviceBindingList 是一个账号的绑定名单，按绑定先后排列；没有绑定时 Bindings 为空切片。
type DeviceBindingList struct {
	UserID   int64           `json:"user_id"`
	Bindings []DeviceBinding `json:"bindings"`
}

// DeviceBindings 列出 userID 的设备绑定名单。不检查账号是否存在：不存在或从未绑定的账号都返回
// 空名单。
func (s *Service) DeviceBindings(ctx context.Context, userID int64) (DeviceBindingList, error) {
	if userID <= 0 {
		return DeviceBindingList{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.deviceLimits == nil {
		return DeviceBindingList{}, fmt.Errorf("admin device limit dependency is not configured")
	}
	rows, err := s.deviceLimits.ListDeviceBindings(ctx, userID)
	if err != nil {
		return DeviceBindingList{}, err
	}
	out := DeviceBindingList{UserID: userID, Bindings: make([]DeviceBinding, 0, len(rows))}
	for _, b := range rows {
		out.Bindings = append(out.Bindings, DeviceBinding{
			DeviceID:    b.DeviceID,
			DeviceModel: b.DeviceModel,
			Platform:    b.Platform,
			AppVersion:  b.AppVersion,
			BoundAt:     b.BoundAt,
			LastUsedAt:  b.LastUsedAt,
		})
	}
	return out, nil
}
