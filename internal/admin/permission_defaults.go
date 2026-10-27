package admin

import (
	"context"
	"fmt"
	"slices"

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
	PermissionsRequest
	ApplyToSelfSignup *bool `json:"apply_to_self_signup,omitempty"`
}

func (req SetPermissionDefaultsRequest) update() permissions.DefaultsUpdate {
	return permissions.DefaultsUpdate{Update: req.PermissionsRequest.update(), ApplyToSelfSignup: req.ApplyToSelfSignup}
}

// PermissionDefaultsResult 是默认权限模板；Found=false 表示没有模板（新账号不受控制）。
type PermissionDefaultsResult struct {
	Found bool `json:"found"`
	PermissionsSettings
	ApplyToSelfSignup bool   `json:"apply_to_self_signup"`
	UpdatedAt         string `json:"updated_at,omitempty"`
	CommandID         string `json:"command_id,omitempty"`
}

func permissionDefaultsResult(d domain.PermissionDefaults, found bool) PermissionDefaultsResult {
	return PermissionDefaultsResult{
		Found:               found,
		PermissionsSettings: permissionsSettings(d.Enabled, d.Flags, d.SendWhitelistEnabled, d.SendWhitelist),
		ApplyToSelfSignup:   d.ApplyToSelfSignup,
		UpdatedAt:           formatUpdatedAt(d.UpdatedAt),
		CommandID:           d.CommandID,
	}
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
	prev, prevFound, next, err := s.permissionDefaults.ResolveDefaults(ctx, req.update())
	if err != nil {
		return CommandResult{}, permissionsError(err)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetPermissionDefaults, 0, domain.Peer{}, req, func() (CommandResult, error) {
		resolved := next.Defaults()
		details := map[string]any{
			"previous_defaults": permissionDefaultsResult(prev, prevFound),
			"defaults":          permissionDefaultsResult(resolved, true),
			"would_change":      !prevFound || !sameDefaults(prev, resolved),
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		prev, prevFound, written, err := s.permissionDefaults.SetDefaults(ctx, next, req.CommandID)
		if err != nil {
			return CommandResult{Details: details}, permissionsError(err)
		}
		details["previous_defaults"] = permissionDefaultsResult(prev, prevFound)
		details["defaults"] = permissionDefaultsResult(written, true)
		details["would_change"] = !prevFound || !sameDefaults(prev, written)
		return CommandResult{Message: "permission defaults updated", Details: details}, nil
	})
}

func sameDefaults(a, b domain.PermissionDefaults) bool {
	return a.Enabled == b.Enabled && a.Flags == b.Flags && a.SendWhitelistEnabled == b.SendWhitelistEnabled &&
		a.ApplyToSelfSignup == b.ApplyToSelfSignup && slices.Equal(a.SendWhitelist, b.SendWhitelist)
}
