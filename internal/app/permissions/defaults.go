package permissions

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// DefaultsService 是默认权限模板（#161）的业务模块：读取模板、解析并校验后台提交的设置、在锁内
// 合并写入。模板只在建号那一刻被复制（domain.PermissionDefaults.Snapshot），不进权限视图缓存。
type DefaultsService struct {
	store store.PermissionDefaultsStore
	users UserReader
}

// NewDefaultsService 创建默认权限模板服务。
func NewDefaultsService(st store.PermissionDefaultsStore, users UserReader) *DefaultsService {
	return &DefaultsService{store: st, users: users}
}

// PermissionDefaults 读取模板；没有模板时返回 domain.DefaultPermissionDefaults 与 found=false。
func (s *DefaultsService) PermissionDefaults(ctx context.Context) (domain.PermissionDefaults, bool, error) {
	d, found, err := s.store.GetPermissionDefaults(ctx)
	if err != nil {
		return domain.PermissionDefaults{}, false, err
	}
	if !found {
		return domain.DefaultPermissionDefaults(), false, nil
	}
	return d, true, nil
}

// DefaultsUpdate 是后台提交的模板设置：与 Update 一样给出的字段整体替换当前值（白名单给出即
// 替换整份名单），未给出的沿用当前值，没有模板时取 domain.DefaultPermissionDefaults。
type DefaultsUpdate struct {
	Update
	ApplyToSelfSignup *bool
}

// ResolvedDefaults 是经 ResolveDefaults 校验过的一次提交。只有 ResolveDefaults 能构造它。
type ResolvedDefaults struct {
	up      DefaultsUpdate
	preview domain.PermissionDefaults
}

// Defaults 返回按 ResolveDefaults 时读到的当前值合并出的模板，供试运行展示。真正写入时
// SetDefaults 在锁内基于那一刻的当前值重新合并。
func (r ResolvedDefaults) Defaults() domain.PermissionDefaults {
	d := r.preview
	d.SendWhitelist = slices.Clone(d.SendWhitelist)
	return d
}

// ResolveDefaults 读取当前模板（没有时为默认值与 found=false），按账号权限的规则校验并去重给出的
// 白名单（存在、未注销、不超过上限；模板没有自身 id），并算出合并后的预览。白名单不合法返回
// domain.ErrPermissionWhitelistInvalid。只读。
func (s *DefaultsService) ResolveDefaults(ctx context.Context, up DefaultsUpdate) (current domain.PermissionDefaults, found bool, next ResolvedDefaults, err error) {
	current, found, err = s.PermissionDefaults(ctx)
	if err != nil {
		return domain.PermissionDefaults{}, false, ResolvedDefaults{}, err
	}
	if up.SendWhitelist != nil {
		whitelist, err := validateWhitelist(ctx, s.users, 0, *up.SendWhitelist)
		if err != nil {
			return domain.PermissionDefaults{}, false, ResolvedDefaults{}, err
		}
		up.SendWhitelist = &whitelist
	}
	return current, found, ResolvedDefaults{up: up, preview: up.apply(current)}, nil
}

// SetDefaults 写入 ResolveDefaults 校验过的提交并记下 commandID；合并在 store 的锁内进行。prev 是
// 合并时读到的当前值（没有模板时为默认值与 prevFound=false）。
func (s *DefaultsService) SetDefaults(ctx context.Context, r ResolvedDefaults, commandID string) (prev domain.PermissionDefaults, prevFound bool, written domain.PermissionDefaults, err error) {
	prev, prevFound, written, err = s.store.UpdatePermissionDefaults(ctx, func(current domain.PermissionDefaults, found bool) domain.PermissionDefaults {
		if !found {
			current = domain.DefaultPermissionDefaults()
		}
		next := r.up.apply(current)
		next.CommandID = commandID
		return next
	})
	if errors.Is(err, domain.ErrUserNotFound) {
		// 模板没有自身账号，store 报"不存在"只能是白名单成员在校验之后消失了。
		err = fmt.Errorf("%w: %w", domain.ErrPermissionWhitelistInvalid, err)
	}
	if err != nil {
		return domain.PermissionDefaults{}, false, domain.PermissionDefaults{}, err
	}
	if !prevFound {
		prev = domain.DefaultPermissionDefaults()
	}
	return prev, prevFound, written, nil
}

// apply 与 Update.apply 用同一套合并规则，再叠加"自助注册也套用"。
func (up DefaultsUpdate) apply(current domain.PermissionDefaults) domain.PermissionDefaults {
	merged := up.Update.apply(0, domain.AccountPermissions{
		Enabled:              current.Enabled,
		Flags:                current.Flags,
		SendWhitelistEnabled: current.SendWhitelistEnabled,
		SendWhitelist:        current.SendWhitelist,
	})
	out := domain.PermissionDefaults{
		Enabled:              merged.Enabled,
		Flags:                merged.Flags,
		SendWhitelistEnabled: merged.SendWhitelistEnabled,
		SendWhitelist:        merged.SendWhitelist,
		ApplyToSelfSignup:    current.ApplyToSelfSignup,
	}
	if up.ApplyToSelfSignup != nil {
		out.ApplyToSelfSignup = *up.ApplyToSelfSignup
	}
	return out
}
