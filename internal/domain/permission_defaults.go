package domain

import (
	"slices"
	"time"
)

// PermissionDefaults 是默认权限模板（#156/#161）：一份与账号权限同形的设置（没有账号 id），
// 加上"自助注册也套用"开关。模板是快照：建号那一刻经 Snapshot 复制给新账号，之后修改模板不影响
// 已有账号。store 里没有模板行就表示没有模板。SendWhitelist 按用户 id 升序、无重复。
type PermissionDefaults struct {
	Enabled              bool
	Flags                AccountPermissionFlags
	SendWhitelistEnabled bool
	SendWhitelist        []int64
	// ApplyToSelfSignup 为 true 时，员工自己在客户端注册（验证码、密码）的账号也套用模板；
	// 后台建号不受它影响。
	ApplyToSelfSignup bool
	// UpdatedAt 与 CommandID 由 store 在写入时记录：最近一次写入的时间与后台命令 id。
	UpdatedAt time.Time
	CommandID string
}

// DefaultPermissionDefaults 返回还没有模板时的设置：与账号没有权限记录时一致（总开关关闭、
// 各项允许、白名单关闭且为空），自助注册不套用。
func DefaultPermissionDefaults() PermissionDefaults {
	return PermissionDefaults{Flags: DefaultAccountPermissionFlags(), SendWhitelist: []int64{}}
}

// Snapshot 返回模板复制给新账号的权限快照（建号时作为 store.AccountExtras.Permissions 传入）。
// UserID 留空由建号的 store 填为新账号的 id；UpdatedAt 留空由 store 记为写入时间；CommandID
// 取建号命令的 id（自助注册时为空）。白名单是独立副本。
func (d PermissionDefaults) Snapshot(commandID string) *AccountPermissions {
	return &AccountPermissions{
		Enabled:              d.Enabled,
		Flags:                d.Flags,
		SendWhitelistEnabled: d.SendWhitelistEnabled,
		SendWhitelist:        NormalizeSendWhitelist(slices.Clone(d.SendWhitelist)),
		CommandID:            commandID,
	}
}
