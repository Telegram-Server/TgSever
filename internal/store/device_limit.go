package store

import (
	"context"

	"telesrv/internal/domain"
)

// DeviceLimitStore 按账号读写设备上限（#104），并维护设备绑定名单（#112）。没有上限即
// "不限制"。上限的执行在 AuthorizationStore.Bind 与 MarkPasswordPassed 里：在用户锁下按
// "绑定规则"判定并写入绑定，拒绝时不写入任何行。memory 与 postgres 的 AuthorizationStore
// 同时实现这个接口，保证读写上限、绑定名单与登录时的判定看到的是同一份数据。
//
// 绑定规则（对一次"完成登录"）：
//  1. bot 或账号没有上限 → 放行，不写绑定；
//  2. 授权处于 password_pending → 放行，不判、不写绑定（转正时再判）；
//  3. 授权所在 auth key 没有合法 device_id → domain.ErrDeviceIDRequired；
//  4. device_id 已在该账号的绑定名单里 → 放行，刷新最近使用时间；
//  5. 绑定数 < 上限 → 写入绑定并放行；
//  6. 否则 → domain.ErrDeviceLimitReached。
type DeviceLimitStore interface {
	// GetDeviceLimit 返回账号的设备上限；found=false 表示没有设置（不限制）。
	GetDeviceLimit(ctx context.Context, userID int64) (limit int, found bool, err error)
	// SetDeviceLimit 设置或覆盖账号的设备上限。limit 不在 [domain.MinDeviceLimit,
	// domain.MaxDeviceLimit] 内返回 domain.ErrDeviceLimitInvalid；账号不存在返回
	// domain.ErrUserNotFound。已登录的设备与已有绑定都不受影响（不踢设备、不删绑定）。
	SetDeviceLimit(ctx context.Context, userID int64, limit int) error
	// ClearDeviceLimit 清除账号的设备上限；cleared 报告之前是否设置过。绑定名单保留，
	// 以后重新设上限时沿用。
	ClearDeviceLimit(ctx context.Context, userID int64) (cleared bool, err error)
	// CheckDeviceLimit 只读地预检：把 authKeyID 作为 userID 的已完成授权（新登录或 2FA 转正）
	// 是否会被绑定规则拒绝。规则与 Bind / MarkPasswordPassed 相同，只是不看 password_pending
	// （预检问的就是"完成之后"），也不写绑定、不刷新最近使用时间。它不加锁，真正的执行点
	// 仍是那两处，这里只用于在有副作用的步骤（如密码找回、扫码确认）之前先拒绝。
	CheckDeviceLimit(ctx context.Context, userID int64, authKeyID [8]byte) error
	// ListDeviceBindings 返回账号的设备绑定名单，按绑定时间先后（同一时刻按 device_id）
	// 排序；没有绑定返回空切片。
	ListDeviceBindings(ctx context.Context, userID int64) ([]domain.DeviceBinding, error)
}

// AuthorizationUserLinker 是可选的进程内组合钩子：授权与用户分别存放在两个内存对象里的
// 实现，借此在 Bind 时读取用户事实（是否 bot），并在设置设备上限时确认账号存在。
// 数据库实现在绑定事务里直接读用户行，不实现这个钩子。
type AuthorizationUserLinker interface {
	LinkAuthorizationUsers(UserStore)
}
