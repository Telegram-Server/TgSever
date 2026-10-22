// Package permissions 是员工权限控制（#118）的业务模块：按账号读取权限视图（已解析总开关、
// bot/系统账号豁免，进程内缓存、跨实例失效），一个纯函数判定 Decide，以及供后台命令调用的
// 设置入口。
//
// 接入新动作的步骤：在 ActionKind 里加一个值，在 Decide 的 switch 里加一个 case 返回对应的
// Denial；不需要目标信息的动作在 rpc.permissionMethodActions（internal/rpc/permission_gate.go）
// 里按方法名登记，需要目标信息的动作在 handler 解析出目标之后调用 Router.checkPermission。
package permissions

import (
	"slices"

	"telesrv/internal/domain"
)

// ActionKind 是一次需要权限判定的操作。
type ActionKind uint8

const (
	actionUnknown ActionKind = iota
	// ActionEdit 是编辑消息（messages.editMessage），不需要目标信息。
	ActionEdit
	// ActionSend 是发送消息（含转发、inline 结果、定时消息立即发送、quick reply 发送、
	// 秘密聊天的建立与发送），需要目标信息：发往收藏夹（TargetSelf）不受限制。
	ActionSend
	// ActionDelete 是删除消息（含开启或修改自动删除计时器），不需要目标信息。
	ActionDelete
	// ActionClearHistory 是清空对话，不需要目标信息。
	ActionClearHistory
)

// TargetKind 是动作作用的会话种类。没有普通群：megagroup、gigagroup、论坛、monoforum 都算
// TargetGroup，只有广播频道算 TargetChannel。
type TargetKind uint8

const (
	// TargetNone 表示动作不需要目标信息（或调用方不知道）。
	TargetNone TargetKind = iota
	TargetGroup
	TargetChannel
	// TargetPrivate 是与另一个用户的私聊，Target.UserID 为对方。
	TargetPrivate
	// TargetSecretChat 是秘密聊天，Target.UserID 为对方。
	TargetSecretChat
	// TargetSelf 是和自己的对话（收藏夹）。
	TargetSelf
)

// Target 是动作的目标信息。
type Target struct {
	Kind TargetKind
	// UserID 是私聊或秘密聊天的对方；其他种类为 0。
	UserID int64
}

// Action 是一次待判定的操作：动作种类 + 必要的目标信息。
type Action struct {
	Kind   ActionKind
	Target Target
}

// Denial 是判定结果：Allowed（空串）表示放行，否则是具体的拒绝原因，取值即 RPC 层返回的
// 403 错误名。
type Denial string

const (
	Allowed           Denial = ""
	DenySend          Denial = "TELESRV_PERMISSION_SEND"
	DenySendWhitelist Denial = "TELESRV_PERMISSION_SEND_WHITELIST"
	DenyDelete        Denial = "TELESRV_PERMISSION_DELETE"
	DenyEdit          Denial = "TELESRV_PERMISSION_EDIT"
	DenyClearHistory  Denial = "TELESRV_PERMISSION_CLEAR_HISTORY"
	DenyCreateGroup   Denial = "TELESRV_PERMISSION_CREATE_GROUP"
	DenyCreateChannel Denial = "TELESRV_PERMISSION_CREATE_CHANNEL"
	DenyJoinGroup     Denial = "TELESRV_PERMISSION_JOIN_GROUP"
	DenyJoinChannel   Denial = "TELESRV_PERMISSION_JOIN_CHANNEL"
	DenyLeaveGroup    Denial = "TELESRV_PERMISSION_LEAVE_GROUP"
	DenyLeaveChannel  Denial = "TELESRV_PERMISSION_LEAVE_CHANNEL"
	DenyAddContact    Denial = "TELESRV_PERMISSION_ADD_CONTACT"
)

// Allowed 报告判定是否放行。
func (d Denial) Allowed() bool { return d == Allowed }

// View 是一个账号此刻生效的权限视图。零值 = 不受控制（没有记录、总开关关闭或豁免账号）。
type View struct {
	enforced    bool
	permissions domain.AccountPermissions
	whitelist   map[int64]struct{}
}

// NewView 由存储的权限设置构造视图；exempt 为 bot 或系统账号，豁免时视图不受控制。
func NewView(p domain.AccountPermissions, exempt bool) View {
	if exempt || !p.Enabled {
		return View{}
	}
	v := View{enforced: true, permissions: p}
	if p.SendWhitelistEnabled {
		v.whitelist = make(map[int64]struct{}, len(p.SendWhitelist))
		for _, id := range p.SendWhitelist {
			v.whitelist[id] = struct{}{}
		}
	}
	return v
}

// Enforced 报告权限控制是否作用于这个账号。
func (v View) Enforced() bool { return v.enforced }

// Permissions 返回生效的权限设置；只在 Enforced 时有意义。
func (v View) Permissions() domain.AccountPermissions {
	p := v.permissions
	p.SendWhitelist = slices.Clone(p.SendWhitelist)
	return p
}

// Decide 是纯函数判定：不受控制的视图一律放行；否则按动作种类查对应的权限项。
// 未登记的动作种类放行（只有接入了判定的动作才会被拦）。
func Decide(v View, a Action) Denial {
	if !v.enforced {
		return Allowed
	}
	flags := v.permissions.Flags
	switch a.Kind {
	case ActionEdit:
		if !flags.Edit {
			return DenyEdit
		}
	case ActionSend:
		// 收藏夹（和自己的对话）不受发送限制。
		if a.Target.Kind == TargetSelf {
			return Allowed
		}
		if !flags.Send {
			return DenySend
		}
	case ActionDelete:
		if !flags.Delete {
			return DenyDelete
		}
	case ActionClearHistory:
		if !flags.ClearHistory {
			return DenyClearHistory
		}
	}
	return Allowed
}
