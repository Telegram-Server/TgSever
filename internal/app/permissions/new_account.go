package permissions

import (
	"context"

	"telesrv/internal/domain"
)

// ResolveNewAccount 解析后台建号（account.create，#162）时给出的权限：给出的字段替换不受控制的
// 默认值（domain.DefaultAccountPermissions），未给出的取默认值，与对一个还没有权限记录的账号
// 设置权限相同。白名单按设置权限的规则校验并去重（存在、未注销、不超过上限；新账号还没有 id，
// 不做"含账号自己"的检查），不合法返回 domain.ErrPermissionWhitelistInvalid。只读；UserID、
// UpdatedAt、CommandID 留空，由建号的 store 与调用方填写。
func (s *Service) ResolveNewAccount(ctx context.Context, up Update) (domain.AccountPermissions, error) {
	up, err := up.withValidWhitelist(ctx, s.users, 0)
	if err != nil {
		return domain.AccountPermissions{}, err
	}
	return up.apply(0, domain.DefaultAccountPermissions(0)), nil
}
