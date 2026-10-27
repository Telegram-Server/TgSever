package admin

import (
	"context"
	"fmt"
	"slices"
	"time"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

// 后台设置默认权限模板（#161）。走普通的命令通道：试运行、按 command id 幂等与冲突检测、写审计。
// 提交的字段整体替换当前值（白名单给出即替换整份名单），未给出的沿用当前值，第一次设置时取默认值
// （总开关关闭、各项允许、白名单关闭且为空、自助注册不套用）。白名单按设置账号权限的规则校验
// （存在、未注销、不超过 domain.MaxSendWhitelistSize；模板没有自身 id），不合法时在进入命令通道
// 之前以 PERMISSION_WHITELIST_INVALID 拒绝。修改模板只影响之后新建的账号。

const ActionSetPermissionDefaults = "permission_defaults.set"

// PermissionDefaultsService 是默认权限模板的业务模块（生产里是 app/permissions.DefaultsService）。
type PermissionDefaultsService interface {
	PermissionDefaults(ctx context.Context) (domain.PermissionDefaults, bool, error)
	ResolveDefaults(ctx context.Context, up permissions.DefaultsUpdate) (current domain.PermissionDefaults, found bool, next permissions.ResolvedDefaults, err error)
	SetDefaults(ctx context.Context, next permissions.ResolvedDefaults, commandID string) (prev domain.PermissionDefaults, prevFound bool, written domain.PermissionDefaults, err error)
}

// SetPermissionDefaultsRequest 是 permission_defaults.set 的参数；字段名与 account.set_permissions
// 一致，另加 apply_to_self_signup。nil = 沿用当前值。
type SetPermissionDefaultsRequest struct {
	CommandMeta
	Enabled           *bool                        `json:"enabled,omitempty"`
	Send              *bool                        `json:"send,omitempty"`
	Delete            *bool                        `json:"delete,omitempty"`
	Edit              *bool                        `json:"edit,omitempty"`
	ClearHistory      *bool                        `json:"clear_history,omitempty"`
	CreateGroup       *bool                        `json:"create_group,omitempty"`
	CreateChannel     *bool                        `json:"create_channel,omitempty"`
	JoinGroup         *bool                        `json:"join_group,omitempty"`
	JoinChannel       *bool                        `json:"join_channel,omitempty"`
	LeaveGroup        *bool                        `json:"leave_group,omitempty"`
	LeaveChannel      *bool                        `json:"leave_channel,omitempty"`
	AddContact        *bool                        `json:"add_contact,omitempty"`
	SendWhitelist     *PermissionsWhitelistRequest `json:"send_whitelist,omitempty"`
	ApplyToSelfSignup *bool                        `json:"apply_to_self_signup,omitempty"`
}

func (req SetPermissionDefaultsRequest) update() permissions.DefaultsUpdate {
	return permissions.DefaultsUpdate{
		Update: SetPermissionsRequest{
			Enabled: req.Enabled,
			Send:    req.Send, Delete: req.Delete, Edit: req.Edit, ClearHistory: req.ClearHistory,
			CreateGroup: req.CreateGroup, CreateChannel: req.CreateChannel,
			JoinGroup: req.JoinGroup, JoinChannel: req.JoinChannel,
			LeaveGroup: req.LeaveGroup, LeaveChannel: req.LeaveChannel,
			AddContact:    req.AddContact,
			SendWhitelist: req.SendWhitelist,
		}.update(),
		ApplyToSelfSignup: req.ApplyToSelfSignup,
	}
}

// PermissionDefaultsResult 是默认权限模板；Found=false 表示没有模板（新账号不受控制）。
type PermissionDefaultsResult struct {
	Found             bool                 `json:"found"`
	Enabled           bool                 `json:"enabled"`
	Send              bool                 `json:"send"`
	Delete            bool                 `json:"delete"`
	Edit              bool                 `json:"edit"`
	ClearHistory      bool                 `json:"clear_history"`
	CreateGroup       bool                 `json:"create_group"`
	CreateChannel     bool                 `json:"create_channel"`
	JoinGroup         bool                 `json:"join_group"`
	JoinChannel       bool                 `json:"join_channel"`
	LeaveGroup        bool                 `json:"leave_group"`
	LeaveChannel      bool                 `json:"leave_channel"`
	AddContact        bool                 `json:"add_contact"`
	SendWhitelist     PermissionsWhitelist `json:"send_whitelist"`
	ApplyToSelfSignup bool                 `json:"apply_to_self_signup"`
	UpdatedAt         string               `json:"updated_at,omitempty"`
	CommandID         string               `json:"command_id,omitempty"`
}

func permissionDefaultsResult(d domain.PermissionDefaults, found bool) PermissionDefaultsResult {
	f := d.Flags
	out := PermissionDefaultsResult{
		Found: found, Enabled: d.Enabled,
		Send: f.Send, Delete: f.Delete, Edit: f.Edit, ClearHistory: f.ClearHistory,
		CreateGroup: f.CreateGroup, CreateChannel: f.CreateChannel,
		JoinGroup: f.JoinGroup, JoinChannel: f.JoinChannel,
		LeaveGroup: f.LeaveGroup, LeaveChannel: f.LeaveChannel,
		AddContact:        f.AddContact,
		SendWhitelist:     PermissionsWhitelist{Enabled: d.SendWhitelistEnabled, UserIDs: domain.NormalizeSendWhitelist(d.SendWhitelist)},
		ApplyToSelfSignup: d.ApplyToSelfSignup,
		CommandID:         d.CommandID,
	}
	if !d.UpdatedAt.IsZero() {
		out.UpdatedAt = d.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// PermissionDefaults 读取默认权限模板；没有模板时返回 found=false 与不受控制的默认值。
func (s *Service) PermissionDefaults(ctx context.Context) (PermissionDefaultsResult, error) {
	if s == nil || s.permissionDefaults == nil {
		return PermissionDefaultsResult{}, fmt.Errorf("admin permission defaults dependency is not configured")
	}
	d, found, err := s.permissionDefaults.PermissionDefaults(ctx)
	if err != nil {
		return PermissionDefaultsResult{}, err
	}
	return permissionDefaultsResult(d, found), nil
}

// SetPermissionDefaults 处理 permission_defaults.set。已有记录的 command id 先按记录重放或报
// COMMAND_ID_CONFLICT；新提交在进入命令通道前合并、校验白名单，不合法时不留命令记录。
func (s *Service) SetPermissionDefaults(ctx context.Context, req SetPermissionDefaultsRequest) (CommandResult, error) {
	if s == nil || s.permissionDefaults == nil {
		return CommandResult{}, fmt.Errorf("admin permission defaults dependency is not configured")
	}
	if result, replayed, err := s.existingCommand(ctx, req.CommandMeta, ActionSetPermissionDefaults, req); replayed || err != nil {
		return result, err
	}
	prev, had, next, err := s.permissionDefaults.ResolveDefaults(ctx, req.update())
	if err != nil {
		return CommandResult{}, permissionsError(err)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetPermissionDefaults, 0, domain.Peer{}, req, func() (CommandResult, error) {
		resolved := next.Defaults()
		details := map[string]any{
			"previous_defaults": permissionDefaultsResult(prev, had),
			"defaults":          permissionDefaultsResult(resolved, true),
			"would_change":      !had || !sameDefaults(prev, resolved),
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		prev, had, written, err := s.permissionDefaults.SetDefaults(ctx, next, req.CommandID)
		if err != nil {
			return CommandResult{Details: details}, permissionsError(err)
		}
		details["previous_defaults"] = permissionDefaultsResult(prev, had)
		details["defaults"] = permissionDefaultsResult(written, true)
		details["would_change"] = !had || !sameDefaults(prev, written)
		return CommandResult{Message: "permission defaults updated", Details: details}, nil
	})
}

func sameDefaults(a, b domain.PermissionDefaults) bool {
	return a.Enabled == b.Enabled && a.Flags == b.Flags && a.SendWhitelistEnabled == b.SendWhitelistEnabled &&
		a.ApplyToSelfSignup == b.ApplyToSelfSignup && slices.Equal(a.SendWhitelist, b.SendWhitelist)
}
