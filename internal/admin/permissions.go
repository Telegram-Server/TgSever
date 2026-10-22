package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

// 后台设置员工权限（#118/#119）。走普通的命令通道：试运行、按 command id 幂等与冲突检测、
// 写审计。提交的字段整体替换当前值（白名单给出即替换整份名单），未给出的字段沿用当前值，
// 第一次设置时取默认值（各项允许、白名单关闭且为空）——所以只提交 enabled=false 就是"关闭
// 总开关、保留各项设置"。系统账号、bot、已注销与不存在的账号返回 USER_INVALID；白名单超过
// domain.MaxSendWhitelistSize、含不存在或已注销的账号、或含账号自己时在进入命令通道之前以
// PERMISSION_WHITELIST_INVALID 拒绝；名单自动去重。写入成功后（包括关闭总开关）经
// PermissionsNotifier 向该账号的在线会话推送 updateConfig。

const ActionSetPermissions = "account.set_permissions"

// AccountPermissionsService 是员工权限业务模块（生产里是 app/permissions.Service）。
type AccountPermissionsService interface {
	AccountPermissions(ctx context.Context, userID int64) (domain.AccountPermissions, bool, error)
	Resolve(ctx context.Context, userID int64, up permissions.Update) (current domain.AccountPermissions, found bool, next permissions.Resolved, err error)
	Set(ctx context.Context, next permissions.Resolved, commandID string) (prev domain.AccountPermissions, prevFound bool, written domain.AccountPermissions, err error)
}

// AccountPermissionsNotifier 在权限写入后通知在线会话（生产里是 rpc.Router：失效本实例缓存
// 并推送 updateConfig）。
type AccountPermissionsNotifier interface {
	NotifyAccountPermissionsChanged(ctx context.Context, userID int64) error
}

// PermissionsWhitelistRequest 是提交的发送白名单；UserIDs 给出（包括空数组）即替换整份名单。
type PermissionsWhitelistRequest struct {
	Enabled *bool    `json:"enabled,omitempty"`
	UserIDs *[]int64 `json:"user_ids,omitempty"`
}

// SetPermissionsRequest 是 account.set_permissions 的参数；字段名与 appConfig 的
// telesrv_permissions 一致。nil = 沿用当前值。
type SetPermissionsRequest struct {
	CommandMeta
	UserID        int64                        `json:"user_id"`
	Enabled       *bool                        `json:"enabled,omitempty"`
	Send          *bool                        `json:"send,omitempty"`
	Delete        *bool                        `json:"delete,omitempty"`
	Edit          *bool                        `json:"edit,omitempty"`
	ClearHistory  *bool                        `json:"clear_history,omitempty"`
	CreateGroup   *bool                        `json:"create_group,omitempty"`
	CreateChannel *bool                        `json:"create_channel,omitempty"`
	JoinGroup     *bool                        `json:"join_group,omitempty"`
	JoinChannel   *bool                        `json:"join_channel,omitempty"`
	LeaveGroup    *bool                        `json:"leave_group,omitempty"`
	LeaveChannel  *bool                        `json:"leave_channel,omitempty"`
	AddContact    *bool                        `json:"add_contact,omitempty"`
	SendWhitelist *PermissionsWhitelistRequest `json:"send_whitelist,omitempty"`
}

func (req SetPermissionsRequest) update() permissions.Update {
	up := permissions.Update{
		Enabled: req.Enabled,
		Flags: permissions.FlagsUpdate{
			Send: req.Send, Delete: req.Delete, Edit: req.Edit, ClearHistory: req.ClearHistory,
			CreateGroup: req.CreateGroup, CreateChannel: req.CreateChannel,
			JoinGroup: req.JoinGroup, JoinChannel: req.JoinChannel,
			LeaveGroup: req.LeaveGroup, LeaveChannel: req.LeaveChannel,
			AddContact: req.AddContact,
		},
	}
	if req.SendWhitelist != nil {
		up.SendWhitelistEnabled = req.SendWhitelist.Enabled
		up.SendWhitelist = req.SendWhitelist.UserIDs
	}
	return up
}

// PermissionsWhitelist 是读出的发送白名单（user_ids 按 id 升序，没有时为空数组）。
type PermissionsWhitelist struct {
	Enabled bool    `json:"enabled"`
	UserIDs []int64 `json:"user_ids"`
}

// AccountPermissionsResult 是账号的权限设置；Found=false 表示没有记录（总开关关闭、各项默认值）。
type AccountPermissionsResult struct {
	UserID        int64                `json:"user_id"`
	Found         bool                 `json:"found"`
	Enabled       bool                 `json:"enabled"`
	Send          bool                 `json:"send"`
	Delete        bool                 `json:"delete"`
	Edit          bool                 `json:"edit"`
	ClearHistory  bool                 `json:"clear_history"`
	CreateGroup   bool                 `json:"create_group"`
	CreateChannel bool                 `json:"create_channel"`
	JoinGroup     bool                 `json:"join_group"`
	JoinChannel   bool                 `json:"join_channel"`
	LeaveGroup    bool                 `json:"leave_group"`
	LeaveChannel  bool                 `json:"leave_channel"`
	AddContact    bool                 `json:"add_contact"`
	SendWhitelist PermissionsWhitelist `json:"send_whitelist"`
	UpdatedAt     string               `json:"updated_at,omitempty"`
	CommandID     string               `json:"command_id,omitempty"`
}

func accountPermissionsResult(p domain.AccountPermissions, found bool) AccountPermissionsResult {
	ids := domain.NormalizeSendWhitelist(p.SendWhitelist)
	f := p.Flags
	out := AccountPermissionsResult{
		UserID: p.UserID, Found: found, Enabled: p.Enabled,
		Send: f.Send, Delete: f.Delete, Edit: f.Edit, ClearHistory: f.ClearHistory,
		CreateGroup: f.CreateGroup, CreateChannel: f.CreateChannel,
		JoinGroup: f.JoinGroup, JoinChannel: f.JoinChannel,
		LeaveGroup: f.LeaveGroup, LeaveChannel: f.LeaveChannel,
		AddContact:    f.AddContact,
		SendWhitelist: PermissionsWhitelist{Enabled: p.SendWhitelistEnabled, UserIDs: ids},
		CommandID:     p.CommandID,
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// AccountPermissions 读取账号当前的权限设置；没有记录时返回总开关关闭与默认值。
func (s *Service) AccountPermissions(ctx context.Context, userID int64) (AccountPermissionsResult, error) {
	if userID <= 0 {
		return AccountPermissionsResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.permissions == nil {
		return AccountPermissionsResult{}, fmt.Errorf("admin permissions dependency is not configured")
	}
	p, found, err := s.permissions.AccountPermissions(ctx, userID)
	if err != nil {
		return AccountPermissionsResult{}, err
	}
	return accountPermissionsResult(p, found), nil
}

// SetPermissions 处理 account.set_permissions。已有记录的 command id 先按记录重放或报
// COMMAND_ID_CONFLICT；新提交在进入命令通道前合并、校验白名单（一次 users.ByIDs），不合法时
// 不留命令记录。
func (s *Service) SetPermissions(ctx context.Context, req SetPermissionsRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.permissions == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin permissions dependency is not configured")
	}
	if result, replayed, err := s.existingCommand(ctx, req.CommandMeta, ActionSetPermissions, req); replayed || err != nil {
		return result, err
	}
	prev, had, next, err := s.permissions.Resolve(ctx, req.UserID, req.update())
	if err != nil {
		return CommandResult{}, permissionsError(err)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetPermissions, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		if err := s.checkPermissionsTarget(ctx, req.UserID); err != nil {
			return CommandResult{}, err
		}
		resolved := next.Permissions()
		details := map[string]any{
			"previous_permissions": accountPermissionsResult(prev, had),
			"permissions":          accountPermissionsResult(resolved, true),
			"would_change":         !had || !samePermissions(prev, resolved),
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		// 真正写入时在锁内基于那一刻的当前值合并：Resolve 之后别人提交的改动不会被这次覆盖掉，
		// 所以 previous/would_change 以锁内读到的值为准。
		prev, had, written, err := s.permissions.Set(ctx, next, req.CommandID)
		if err != nil {
			return CommandResult{Details: details}, permissionsError(err)
		}
		details["previous_permissions"] = accountPermissionsResult(prev, had)
		details["permissions"] = accountPermissionsResult(written, true)
		details["would_change"] = !had || !samePermissions(prev, written)
		if s.permissionsNotifier != nil {
			if err := s.permissionsNotifier.NotifyAccountPermissionsChanged(ctx, req.UserID); err != nil {
				details["notify_error"] = err.Error()
			}
		}
		return CommandResult{Message: "account permissions updated", Details: details}, nil
	})
}

// checkPermissionsTarget 只接受存在、未注销的普通账号（与设备上限同一套规则）。
func (s *Service) checkPermissionsTarget(ctx context.Context, userID int64) error {
	invalid := codedError("USER_INVALID", fmt.Errorf("user %d cannot have staff permissions", userID))
	if domain.IsSystemUserID(userID) {
		return invalid
	}
	u, found, err := s.users.AdminUser(ctx, userID)
	if err != nil {
		return err
	}
	if !found || u.Deleted || u.Bot {
		return invalid
	}
	return nil
}

func permissionsError(err error) error {
	switch {
	case errors.Is(err, domain.ErrPermissionWhitelistInvalid):
		return codedError("PERMISSION_WHITELIST_INVALID", err)
	case errors.Is(err, domain.ErrUserNotFound):
		return codedError("USER_INVALID", err)
	default:
		return err
	}
}

func samePermissions(a, b domain.AccountPermissions) bool {
	return a.Enabled == b.Enabled && a.Flags == b.Flags && a.SendWhitelistEnabled == b.SendWhitelistEnabled &&
		slices.Equal(a.SendWhitelist, b.SendWhitelist)
}
