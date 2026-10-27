package admin

import (
	"context"
	"fmt"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 后台建号时一起写入员工权限（account.create 的可选 permissions，#162）。账号从诞生起就受控：
// 建号与权限经 store.AccountCreationStore 在同一个事务里写入，任何一步失败都不留下账号。
//
//   - 给出 permissions：字段与 account.set_permissions 一致，给出的替换不受控制的默认值（总开关
//     关闭、各项允许、白名单关闭且为空），未给出的取默认值；白名单按设置权限的规则校验（存在、
//     未注销、不超过 domain.MaxSendWhitelistSize），不合法时在进入命令通道之前以
//     PERMISSION_WHITELIST_INVALID 拒绝：不建号，也不留命令记录（已有记录的 command id 照常重放）。
//     要一个不受控的账号就显式传 enabled=false。
//   - 不给：有默认权限模板就套用模板快照（domain.PermissionDefaults.Snapshot），没有模板就不写
//     权限。这一路不看模板的"自助注册也套用"开关。
//
// 试运行连同权限一起校验，不写入。命令详情记下最终写入的权限（permissions）与来源
// （permissions_source = request / defaults）；不写权限时两个键都没有。

// 命令详情 permissions_source 的取值。
const (
	createAccountPermissionsFromRequest  = "request"
	createAccountPermissionsFromDefaults = "defaults"
)

// newAccountPermissions 算出新账号要写入的权限与来源；返回 nil 表示不写权限。只读。
func (s *Service) newAccountPermissions(ctx context.Context, req CreateAccountRequest) (*domain.AccountPermissions, string, error) {
	if req.Permissions != nil {
		if s.permissions == nil {
			return nil, "", fmt.Errorf("admin permissions dependency is not configured")
		}
		p, err := s.permissions.ResolveNewAccount(ctx, req.Permissions.update())
		if err != nil {
			return nil, "", permissionsError(err)
		}
		p.CommandID = req.CommandID
		return &p, createAccountPermissionsFromRequest, nil
	}
	if s.permissionDefaults == nil {
		return nil, "", nil
	}
	d, found, err := s.permissionDefaults.PermissionDefaults(ctx)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", nil
	}
	return d.Snapshot(req.CommandID), createAccountPermissionsFromDefaults, nil
}

// permissionsAccountCreator 返回"建号并写入权限"的单事务建号步骤（不带登录密码时用）。
func (s *Service) permissionsAccountCreator(perms *domain.AccountPermissions) (auth.CreateAccountFunc, error) {
	if s.accountCreation == nil {
		return nil, fmt.Errorf("admin account creation store is not configured")
	}
	return func(ctx context.Context, u domain.User) (domain.User, error) {
		return s.accountCreation.CreateAccount(ctx, u, store.AccountExtras{Permissions: perms})
	}, nil
}

// writtenAccountPermissions 读回新账号实际写入的权限（store 会排除期间注销的白名单成员）；读不到
// 时退回打算写入的值。
func (s *Service) writtenAccountPermissions(ctx context.Context, userID int64, intended domain.AccountPermissions) domain.AccountPermissions {
	intended.UserID = userID
	if s.permissions == nil {
		return intended
	}
	p, found, err := s.permissions.AccountPermissions(ctx, userID)
	if err != nil || !found {
		return intended
	}
	return p
}

func addCreateAccountPermissionDetails(details map[string]any, perms *domain.AccountPermissions, source string) {
	if perms == nil {
		return
	}
	details["permissions"] = accountPermissionsResult(*perms, true)
	details["permissions_source"] = source
}
