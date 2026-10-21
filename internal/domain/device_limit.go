package domain

import "errors"

// 账号设备上限（#104）：管理员按账号设置最多可登录的设备数，一台设备 = 一条登录授权
// （客户端"设置 → 设备"里的一行）。没设上限的账号不受限制；bot 不受限制。

const (
	// MinDeviceLimit / MaxDeviceLimit 是可设置的上限取值范围；0 和负数非法，"不限制"用清除表达。
	MinDeviceLimit = 1
	MaxDeviceLimit = 100
)

var (
	// ErrDeviceLimitReached 表示账号已完成登录的授权数（不算当前 auth key 自身、不算 2FA
	// 未完成的授权）已达到设备上限，这次绑定被拒绝且没有写入任何行。授权 store 的绑定操作
	// 返回它；RPC 层映射为 400 AUTHORIZATIONS_TOO_MUCH。
	ErrDeviceLimitReached = errors.New("device limit reached")
	// ErrDeviceLimitInvalid 表示设备上限不在 [MinDeviceLimit, MaxDeviceLimit] 内。
	ErrDeviceLimitInvalid = errors.New("device limit invalid")
)

// ValidDeviceLimit 报告 limit 是否是可设置的设备上限。
func ValidDeviceLimit(limit int) bool {
	return limit >= MinDeviceLimit && limit <= MaxDeviceLimit
}
