package permissions

import (
	"context"
	"fmt"
	"slices"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	viewCacheEntries = 100_000
	// viewCacheTTL 只是漏掉通知时的兜底；跨实例失效靠 read model "account_permissions" 通知，
	// 监听器断线重连时整体 Flush。
	viewCacheTTL = 10 * time.Minute
)

// UserReader 是服务读取账号事实（是否存在、已注销、bot）所需的最小 UserStore 子集。
type UserReader interface {
	ByID(ctx context.Context, id int64) (domain.User, bool, error)
	ByIDs(ctx context.Context, ids []int64) ([]domain.User, error)
}

// Service 是权限业务模块：读取权限视图（进程内缓存）、解析并校验后台提交的设置、写入并失效缓存。
type Service struct {
	store store.AccountPermissionsStore
	users UserReader
	views *readmodelcache.Cache[int64, View]
}

// NewService 创建权限服务。
func NewService(st store.AccountPermissionsStore, users UserReader) *Service {
	return &Service{
		store: st,
		users: users,
		views: readmodelcache.New(readmodelcache.Config[int64, View]{MaxEntries: viewCacheEntries, TTL: viewCacheTTL}),
	}
}

// View 返回账号此刻生效的权限视图：没有记录、总开关关闭、bot 与系统账号都不受控制。
// 命中进程内缓存时不访问 store。
func (s *Service) View(ctx context.Context, userID int64) (View, error) {
	if s == nil || userID <= 0 || domain.IsSystemUserID(userID) {
		return View{}, nil
	}
	return s.views.GetOrLoad(ctx, userID, func() (View, error) {
		p, found, err := s.store.GetAccountPermissions(ctx, userID)
		if err != nil {
			return View{}, fmt.Errorf("load account permissions: %w", err)
		}
		if !found || !p.Enabled {
			return View{}, nil
		}
		u, found, err := s.users.ByID(ctx, userID)
		if err != nil {
			return View{}, fmt.Errorf("load account permissions user: %w", err)
		}
		return NewView(p, !found || u.Bot), nil
	})
}

// EnforcedAccountPermissions 返回作用于账号的权限设置；enforced=false 表示不受控制（help 的
// appConfig 叠加层据此决定是否下发 telesrv_permissions）。
func (s *Service) EnforcedAccountPermissions(ctx context.Context, userID int64) (domain.AccountPermissions, bool, error) {
	view, err := s.View(ctx, userID)
	if err != nil || !view.Enforced() {
		return domain.AccountPermissions{}, false, err
	}
	return view.Permissions(), true, nil
}

// AccountPermissions 不经缓存地读取账号存储的权限设置；没有记录时返回
// domain.DefaultAccountPermissions 与 found=false。
func (s *Service) AccountPermissions(ctx context.Context, userID int64) (domain.AccountPermissions, bool, error) {
	p, found, err := s.store.GetAccountPermissions(ctx, userID)
	if err != nil {
		return domain.AccountPermissions{}, false, err
	}
	if !found {
		return domain.DefaultAccountPermissions(userID), false, nil
	}
	return p, true, nil
}

// FlagsUpdate 是 11 项权限的提交值；nil = 沿用当前值（没有记录时默认允许）。
type FlagsUpdate struct {
	Send, Delete, Edit, ClearHistory *bool
	CreateGroup, CreateChannel       *bool
	JoinGroup, JoinChannel           *bool
	LeaveGroup, LeaveChannel         *bool
	AddContact                       *bool
}

// Update 是后台提交的权限设置。给出的字段整体替换当前值（SendWhitelist 给出即替换整份名单）；
// 未给出的字段沿用当前值，没有记录时取默认值（总开关关闭、各项允许、白名单关闭且为空）。
// 所以只提交 Enabled=false 就是"关闭总开关、保留各项设置"。
type Update struct {
	Enabled              *bool
	Flags                FlagsUpdate
	SendWhitelistEnabled *bool
	SendWhitelist        *[]int64
}

// Resolve 把 update 叠加到账号当前的设置上，并校验、去重白名单。白名单不合法返回
// domain.ErrPermissionWhitelistInvalid。只读，不写 store。
func (s *Service) Resolve(ctx context.Context, userID int64, up Update) (domain.AccountPermissions, error) {
	current, _, err := s.AccountPermissions(ctx, userID)
	if err != nil {
		return domain.AccountPermissions{}, err
	}
	p := current
	p.UserID = userID
	p.UpdatedAt = time.Time{}
	p.CommandID = ""
	apply := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	apply(&p.Enabled, up.Enabled)
	f := up.Flags
	apply(&p.Flags.Send, f.Send)
	apply(&p.Flags.Delete, f.Delete)
	apply(&p.Flags.Edit, f.Edit)
	apply(&p.Flags.ClearHistory, f.ClearHistory)
	apply(&p.Flags.CreateGroup, f.CreateGroup)
	apply(&p.Flags.CreateChannel, f.CreateChannel)
	apply(&p.Flags.JoinGroup, f.JoinGroup)
	apply(&p.Flags.JoinChannel, f.JoinChannel)
	apply(&p.Flags.LeaveGroup, f.LeaveGroup)
	apply(&p.Flags.LeaveChannel, f.LeaveChannel)
	apply(&p.Flags.AddContact, f.AddContact)
	apply(&p.SendWhitelistEnabled, up.SendWhitelistEnabled)
	if up.SendWhitelist != nil {
		p.SendWhitelist = *up.SendWhitelist
	}
	whitelist, err := s.validateWhitelist(ctx, userID, p.SendWhitelist)
	if err != nil {
		return domain.AccountPermissions{}, err
	}
	p.SendWhitelist = whitelist
	return p, nil
}

// Set 整体替换账号的权限设置（白名单同样校验、去重），成功后失效本实例缓存；其他实例经
// store 写入时发出的读模型通知失效。
func (s *Service) Set(ctx context.Context, p domain.AccountPermissions) (domain.AccountPermissions, error) {
	whitelist, err := s.validateWhitelist(ctx, p.UserID, p.SendWhitelist)
	if err != nil {
		return domain.AccountPermissions{}, err
	}
	p.SendWhitelist = whitelist
	out, err := s.store.SetAccountPermissions(ctx, p)
	s.InvalidateAccountPermissions(p.UserID)
	if err != nil {
		return domain.AccountPermissions{}, err
	}
	return out, nil
}

// InvalidateAccountPermissions 失效账号的缓存视图（读模型通知与本地写入后调用）。
func (s *Service) InvalidateAccountPermissions(userID int64) {
	if s != nil {
		s.views.Invalidate(userID)
	}
}

// FlushAccountPermissions 清空全部缓存视图（读模型监听器断线重连时调用）。
func (s *Service) FlushAccountPermissions() {
	if s != nil {
		s.views.Flush()
	}
}

// validateWhitelist 去重并按 id 升序返回名单；超过上限、含账号自己、含不存在或已注销的账号时
// 返回 domain.ErrPermissionWhitelistInvalid。
func (s *Service) validateWhitelist(ctx context.Context, userID int64, ids []int64) ([]int64, error) {
	out := slices.Clone(ids)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []int64{}
	}
	if len(out) > domain.MaxSendWhitelistSize {
		return nil, fmt.Errorf("%w: more than %d accounts", domain.ErrPermissionWhitelistInvalid, domain.MaxSendWhitelistSize)
	}
	if _, self := slices.BinarySearch(out, userID); self {
		return nil, fmt.Errorf("%w: contains the account itself", domain.ErrPermissionWhitelistInvalid)
	}
	if len(out) == 0 {
		return out, nil
	}
	users, err := s.users.ByIDs(ctx, out)
	if err != nil {
		return nil, fmt.Errorf("load whitelist accounts: %w", err)
	}
	live := make(map[int64]bool, len(users))
	for _, u := range users {
		live[u.ID] = !u.Deleted
	}
	for _, id := range out {
		if !live[id] {
			return nil, fmt.Errorf("%w: account %d does not exist or is deleted", domain.ErrPermissionWhitelistInvalid, id)
		}
	}
	return out, nil
}
