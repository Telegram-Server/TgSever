package store

import "context"

// DeviceLimitStore 按账号读写设备上限（#104）。没有上限即"不限制"。上限的执行在
// AuthorizationStore.Bind 里：绑定事务锁住用户行后计数再插入，超限返回
// domain.ErrDeviceLimitReached，不写入任何行。memory 与 postgres 的 AuthorizationStore
// 同时实现这个接口，保证读写上限与绑定时的计数看到的是同一份数据。
type DeviceLimitStore interface {
	// GetDeviceLimit 返回账号的设备上限；found=false 表示没有设置（不限制）。
	GetDeviceLimit(ctx context.Context, userID int64) (limit int, found bool, err error)
	// SetDeviceLimit 设置或覆盖账号的设备上限。limit 不在 [domain.MinDeviceLimit,
	// domain.MaxDeviceLimit] 内返回 domain.ErrDeviceLimitInvalid；账号不存在返回
	// domain.ErrUserNotFound。已登录的设备不受影响（不踢设备）。
	SetDeviceLimit(ctx context.Context, userID int64, limit int) error
	// ClearDeviceLimit 清除账号的设备上限；cleared 报告之前是否设置过。
	ClearDeviceLimit(ctx context.Context, userID int64) (cleared bool, err error)
	// CheckDeviceLimit 只读地预检：把 authKeyID 绑定（或转正）为 userID 的已完成授权是否会
	// 超出上限，超限返回 domain.ErrDeviceLimitReached。规则与 Bind / MarkPasswordPassed 相同；
	// 它不加锁，真正的执行点仍是那两处，这里只用于在有副作用的步骤（如密码找回）之前先拒绝。
	CheckDeviceLimit(ctx context.Context, userID int64, authKeyID [8]byte) error
}

// AuthorizationUserLinker 是可选的进程内组合钩子：授权与用户分别存放在两个内存对象里的
// 实现，借此在 Bind 时读取用户事实（是否 bot），并在设置设备上限时确认账号存在。
// 数据库实现在绑定事务里直接读用户行，不实现这个钩子。
type AuthorizationUserLinker interface {
	LinkAuthorizationUsers(UserStore)
}
