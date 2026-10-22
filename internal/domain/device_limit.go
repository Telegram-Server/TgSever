package domain

import (
	"errors"
	"time"
)

// 账号设备上限（#104）与设备绑定（#112）：管理员按账号设置最多可使用的设备数。设了上限的
// 账号，一台设备（按客户端上报的 device_id 识别）第一次在该账号上完成登录即被绑定，名额 =
// 绑定记录数；退出登录、撤销授权都不解除绑定。没设上限的账号不受限制、不记录绑定；bot
// 不受限制。

const (
	// MinDeviceLimit / MaxDeviceLimit 是可设置的上限取值范围；0 和负数非法，"不限制"用清除表达。
	MinDeviceLimit = 1
	MaxDeviceLimit = 100
)

var (
	// ErrDeviceLimitReached 表示账号已绑定的设备数已达到设备上限，而这次登录的设备不在绑定
	// 名单里；这次登录被拒绝且没有写入任何行。授权 store 的绑定操作返回它；RPC 层映射为
	// 400 AUTHORIZATIONS_TOO_MUCH。
	ErrDeviceLimitReached = errors.New("device limit reached")
	// ErrDeviceIDRequired 表示账号设了设备上限，而这次登录所在的 auth key 没有上报过合法的
	// device_id，无法判断是哪台设备；这次登录被拒绝且没有写入任何行。RPC 层映射为
	// 400 DEVICE_ID_REQUIRED。
	ErrDeviceIDRequired = errors.New("device id required")
	// ErrDeviceLimitInvalid 表示设备上限不在 [MinDeviceLimit, MaxDeviceLimit] 内。
	ErrDeviceLimitInvalid = errors.New("device limit invalid")
)

// ValidDeviceLimit 报告 limit 是否是可设置的设备上限。
func ValidDeviceLimit(limit int) bool {
	return limit >= MinDeviceLimit && limit <= MaxDeviceLimit
}

// DeviceBinding 是账号设备绑定名单里的一行（#112）：主键 (UserID, DeviceID)。设备型号、
// 平台、客户端版本取自绑定那一刻的登录，之后不随登录刷新；LastUsedAt 在已绑定设备每次
// 完成登录时刷新。
type DeviceBinding struct {
	UserID      int64
	DeviceID    string
	DeviceModel string
	Platform    string
	AppVersion  string
	BoundAt     time.Time
	LastUsedAt  time.Time
}
