package domain

import (
	"errors"
	"time"
)

// 员工权限控制（#118）：管理员按账号打开总开关后，按 11 项"是否允许"的权限与一份发送白名单
// 限制账号可用的操作。没有记录或总开关关闭时完全不生效；bot 与系统账号不受控制。

// MaxSendWhitelistSize 是发送白名单的人数上限。
const MaxSendWhitelistSize = 1000

var (
	// ErrPermissionWhitelistInvalid 表示白名单超过上限、含不存在或已注销的账号、或含账号自己；
	// 后台命令映射为 PERMISSION_WHITELIST_INVALID。
	ErrPermissionWhitelistInvalid = errors.New("permission whitelist invalid")
)

// AccountPermissionFlags 是 11 项操作权限，true = 允许。零值表示"全部禁止"，
// 默认值（全部允许）用 DefaultAccountPermissionFlags。
type AccountPermissionFlags struct {
	Send          bool
	Delete        bool
	Edit          bool
	ClearHistory  bool
	CreateGroup   bool
	CreateChannel bool
	JoinGroup     bool
	JoinChannel   bool
	LeaveGroup    bool
	LeaveChannel  bool
	AddContact    bool
}

// DefaultAccountPermissionFlags 返回首次设置时各项的默认值：全部允许。
func DefaultAccountPermissionFlags() AccountPermissionFlags {
	return AccountPermissionFlags{
		Send: true, Delete: true, Edit: true, ClearHistory: true,
		CreateGroup: true, CreateChannel: true,
		JoinGroup: true, JoinChannel: true,
		LeaveGroup: true, LeaveChannel: true,
		AddContact: true,
	}
}

// AccountPermissions 是一个账号的权限设置（account_permissions 一行 + 发送白名单）。
// 关闭总开关（Enabled=false）不清除各项设置。SendWhitelist 按用户 id 升序、无重复。
type AccountPermissions struct {
	UserID               int64
	Enabled              bool
	Flags                AccountPermissionFlags
	SendWhitelistEnabled bool
	SendWhitelist        []int64
	// UpdatedAt 与 CommandID 由 store 在写入时记录：最近一次写入的时间与后台命令 id。
	UpdatedAt time.Time
	CommandID string
}

// DefaultAccountPermissions 返回账号没有记录时的权限设置：总开关关闭、各项允许、白名单关闭且为空。
func DefaultAccountPermissions(userID int64) AccountPermissions {
	return AccountPermissions{UserID: userID, Flags: DefaultAccountPermissionFlags(), SendWhitelist: []int64{}}
}
