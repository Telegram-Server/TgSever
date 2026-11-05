package domain

import (
	"errors"
	"time"
)

// 游客注册（telesrv.signUpAsGuest，docs/guest-login.md）：不带手机号注册，服务端分配号码。

var (
	// ErrGuestLoginDisabled 表示服务器没有开启游客登录；RPC 层映射为 400 GUEST_LOGIN_DISABLED。
	ErrGuestLoginDisabled = errors.New("guest login disabled")
	// ErrGuestIPUnavailable 表示拿不到请求的客户端 IP，无法按 IP 绑定，拒绝注册；
	// RPC 层映射为 400 GUEST_IP_UNAVAILABLE。
	ErrGuestIPUnavailable = errors.New("guest sign-up client ip unavailable")
	// ErrGuestNumberPoolExhausted 表示多次随机都没有取到空闲的游客号码；
	// RPC 层映射为 500 GUEST_NUMBER_POOL_EXHAUSTED。
	ErrGuestNumberPoolExhausted = errors.New("guest number pool exhausted")
	// ErrGuestIPBindingChanged 是存储层的比较并交换失败：建号时 IP 绑定已被别的请求换掉，
	// 整个建号回滚。游客注册服务据此重读绑定，不会返回给客户端。
	ErrGuestIPBindingChanged = errors.New("guest ip binding changed concurrently")
)

// 游客 IP 绑定天数（同一 IP 在这么多天内再次游客注册得到同一个账号）的取值范围与默认值。
const (
	GuestIPBindDaysMin     = 1
	GuestIPBindDaysMax     = 365
	GuestIPBindDaysDefault = 7
)

// ValidGuestIPBindDays 报告天数是否在允许范围内。
func ValidGuestIPBindDays(days int) bool {
	return days >= GuestIPBindDaysMin && days <= GuestIPBindDaysMax
}

// GuestLoginSettings 是游客登录开关与 IP 绑定天数的实际值（管理面板覆盖与环境变量合并后）。
type GuestLoginSettings struct {
	Enabled    bool
	IPBindDays int
}

// IPBindWindow 返回 IP 绑定的有效时长。
func (s GuestLoginSettings) IPBindWindow() time.Duration {
	return time.Duration(s.IPBindDays) * 24 * time.Hour
}

// ResolveGuestLogin 合并游客登录的两项设置：管理面板覆盖值（非 nil）优先，否则跟随环境变量。
// 天数覆盖值不在允许范围内时视为未设置；环境变量也不合法时回到默认值。服务端判断与管理面板
// 展示共用这一处。
func ResolveGuestLogin(enabledOverride *bool, envEnabled bool, daysOverride *int, envDays int) GuestLoginSettings {
	days := envDays
	if !ValidGuestIPBindDays(days) {
		days = GuestIPBindDaysDefault
	}
	if daysOverride != nil && ValidGuestIPBindDays(*daysOverride) {
		days = *daysOverride
	}
	return GuestLoginSettings{Enabled: ResolveOverride(enabledOverride, envEnabled), IPBindDays: days}
}

// GuestAccount 标记一个经游客注册创建的账号。Phone 是注册时分配的号码。
type GuestAccount struct {
	UserID    int64
	Phone     string
	IPKey     string
	CreatedAt time.Time
}

// GuestIPBinding 是一个 IP 键当前绑定的游客账号。
type GuestIPBinding struct {
	IPKey   string
	UserID  int64
	BoundAt time.Time
}
