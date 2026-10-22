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
	// SetDeviceLimit 设置或覆盖账号的设备上限，并在同一把用户锁下补绑（#115）：把账号当前
	// 已完成（非 password_pending）、且 auth key 上有合法 device_id 的授权按登录时间先后
	// 绑定，直到绑定数达到上限；backfilled 是这次新写入的绑定数。已绑定的设备不重复计、
	// 不刷新；没有 device_id 或超出名额的授权保持登录、不绑定；bot 不补绑。limit 不在
	// [domain.MinDeviceLimit, domain.MaxDeviceLimit] 内返回 domain.ErrDeviceLimitInvalid；
	// 账号不存在返回 domain.ErrUserNotFound。不踢设备、不删绑定。
	SetDeviceLimit(ctx context.Context, userID int64, limit int) (backfilled int, err error)
	// PreviewDeviceLimitBackfill 只读地计算把上限设为 limit 时 SetDeviceLimit 会补绑的设备数
	// （供试运行）。不加锁，结果是此刻的估计。
	PreviewDeviceLimitBackfill(ctx context.Context, userID int64, limit int) (int, error)
	// BackfillDeviceBinding 是延迟补绑（#115）：authKeyID 上的已完成授权在之后才持久化了
	// device_id（老客户端升级后重连）。账号设了上限且不是 bot、device_id 合法、尚未绑定
	// 且还有名额时写入绑定，bound=true；其余情况（没有授权、password_pending、未设上限、
	// 已绑定、名额已满）什么都不做，bound=false，也不踢下线。在用户锁下判定，与登录争抢
	// 名额时绑定数不超过上限。
	// 调用方只在 AuthKeyStore.UpdateClientInfo 报告 device_id 本次新写入时调用，不放在每次
	// initConnection 的热路径上。
	BackfillDeviceBinding(ctx context.Context, authKeyID [8]byte) (bound bool, err error)
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
	// UnbindDevice 是后台解绑（#114）：在同一事务、同一把用户锁下删除 userID 对 deviceID 的
	// 绑定，并撤销该账号在 device_id 相同的 auth key 上的全部授权（含 password_pending），
	// 返回被撤销的授权。撤销与远程踢设备相同：只删业务授权，保留协议 key，客户端重连后收到
	// 401 并退出；调用方还要关闭这些 key 上的在线连接。绑定不存在返回
	// domain.ErrDeviceBindingNotFound，不撤销任何授权。别的账号在同一 device_id 上的授权
	// 与绑定不受影响。
	UnbindDevice(ctx context.Context, userID int64, deviceID string) ([]domain.Authorization, error)
	// PreviewUnbindDevice 只读地报告 UnbindDevice 会撤销的授权数（供试运行）；绑定不存在返回
	// domain.ErrDeviceBindingNotFound。不加锁，结果是此刻的估计。
	PreviewUnbindDevice(ctx context.Context, userID int64, deviceID string) (int, error)
}

// AuthorizationUserLinker 是可选的进程内组合钩子：授权与用户分别存放在两个内存对象里的
// 实现，借此在 Bind 时读取用户事实（是否 bot），并在设置设备上限时确认账号存在。
// 数据库实现在绑定事务里直接读用户行，不实现这个钩子。
type AuthorizationUserLinker interface {
	LinkAuthorizationUsers(UserStore)
}
