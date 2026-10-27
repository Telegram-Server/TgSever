package permissions

import (
	"context"
	"errors"
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

// Resolved 是经 Resolve 校验过的一次提交：白名单（若给出）已去重、校验。只有 Resolve 能构造它，
// 所以 Set 不再重复校验白名单（一次提交只查一次 users.ByIDs）。
type Resolved struct {
	userID  int64
	up      Update
	preview domain.AccountPermissions
}

// Permissions 返回按 Resolve 时读到的当前值合并出的设置（白名单已去重、按 id 升序），供试运行
// 展示。真正写入时 Set 会在锁内基于那一刻的当前值重新合并，结果可能与这里不同。
func (r Resolved) Permissions() domain.AccountPermissions {
	p := r.preview
	p.SendWhitelist = slices.Clone(p.SendWhitelist)
	return p
}

// Resolve 读取账号当前的设置（current，没有记录时为默认值与 found=false），校验并去重 update
// 给出的白名单，并算出合并后的预览。白名单不合法返回 domain.ErrPermissionWhitelistInvalid。
// 只读，不写 store。
func (s *Service) Resolve(ctx context.Context, userID int64, up Update) (current domain.AccountPermissions, found bool, next Resolved, err error) {
	current, found, err = s.AccountPermissions(ctx, userID)
	if err != nil {
		return domain.AccountPermissions{}, false, Resolved{}, err
	}
	if up, err = up.withValidWhitelist(ctx, s.users, userID); err != nil {
		return domain.AccountPermissions{}, false, Resolved{}, err
	}
	return current, found, Resolved{userID: userID, up: up, preview: up.apply(userID, current)}, nil
}

// apply 把 update 叠加到 current 上：给出的字段替换当前值，未给出的沿用。
func (up Update) apply(userID int64, current domain.AccountPermissions) domain.AccountPermissions {
	p := current
	p.UserID = userID
	p.UpdatedAt = time.Time{}
	p.CommandID = ""
	p.SendWhitelist = slices.Clone(current.SendWhitelist)
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	set(&p.Enabled, up.Enabled)
	f := up.Flags
	set(&p.Flags.Send, f.Send)
	set(&p.Flags.Delete, f.Delete)
	set(&p.Flags.Edit, f.Edit)
	set(&p.Flags.ClearHistory, f.ClearHistory)
	set(&p.Flags.CreateGroup, f.CreateGroup)
	set(&p.Flags.CreateChannel, f.CreateChannel)
	set(&p.Flags.JoinGroup, f.JoinGroup)
	set(&p.Flags.JoinChannel, f.JoinChannel)
	set(&p.Flags.LeaveGroup, f.LeaveGroup)
	set(&p.Flags.LeaveChannel, f.LeaveChannel)
	set(&p.Flags.AddContact, f.AddContact)
	set(&p.SendWhitelistEnabled, up.SendWhitelistEnabled)
	if up.SendWhitelist != nil {
		p.SendWhitelist = slices.Clone(*up.SendWhitelist)
	}
	p.SendWhitelist = domain.NormalizeSendWhitelist(p.SendWhitelist)
	return p
}

// Set 写入 Resolve 校验过的提交并记下 commandID。合并在 store 的锁内基于那一刻的当前值进行，
// 所以两个操作员同时改不同的项时两份改动都会保留（后提交的不会把先提交的覆盖回去）；prev 是
// 合并时读到的当前值（没有记录时为默认值与 prevFound=false）。成功后失效本实例缓存；其他实例经
// store 写入时发出的读模型通知失效。
func (s *Service) Set(ctx context.Context, r Resolved, commandID string) (prev domain.AccountPermissions, prevFound bool, written domain.AccountPermissions, err error) {
	prev, prevFound, written, err = s.store.UpdateAccountPermissions(ctx, r.userID, func(current domain.AccountPermissions, found bool) domain.AccountPermissions {
		if !found {
			current = domain.DefaultAccountPermissions(r.userID)
		}
		next := r.up.apply(r.userID, current)
		next.CommandID = commandID
		return next
	})
	s.InvalidateAccountPermissions(r.userID)
	if err != nil {
		return domain.AccountPermissions{}, false, domain.AccountPermissions{}, err
	}
	if !prevFound {
		prev = domain.DefaultAccountPermissions(r.userID)
	}
	return prev, prevFound, written, nil
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

// withValidWhitelist 校验 update 给出的白名单并换成去重、按 id 升序的结果；没给出白名单时原样
// 返回。账号权限、后台建号与默认权限模板共用这一步，规则见 validateWhitelist。
func (up Update) withValidWhitelist(ctx context.Context, reader UserReader, selfID int64) (Update, error) {
	if up.SendWhitelist == nil {
		return up, nil
	}
	whitelist, err := validateWhitelist(ctx, reader, selfID, *up.SendWhitelist)
	if err != nil {
		return Update{}, err
	}
	up.SendWhitelist = &whitelist
	return up, nil
}

// WhitelistMemberVanished 把 store 写入时报的 domain.ErrUserNotFound 归为白名单不合法，用于除白名单
// 成员外没有别的账号可能"不存在"的写入（默认权限模板没有自身账号；后台建号时新账号正在创建）：
// 不存在的只能是白名单成员在校验之后消失了。其他错误原样返回。
func WhitelistMemberVanished(err error) error {
	if errors.Is(err, domain.ErrUserNotFound) {
		return fmt.Errorf("%w: %w", domain.ErrPermissionWhitelistInvalid, err)
	}
	return err
}

// validateWhitelist 去重并按 id 升序返回名单；超过上限、含账号自己、含不存在或已注销的账号时
// 返回 domain.ErrPermissionWhitelistInvalid。selfID 为 0 时（模板与新账号没有自身 id）不做"含
// 账号自己"的检查。
func validateWhitelist(ctx context.Context, reader UserReader, selfID int64, ids []int64) ([]int64, error) {
	out := domain.NormalizeSendWhitelist(ids)
	if len(out) > domain.MaxSendWhitelistSize {
		return nil, fmt.Errorf("%w: more than %d accounts", domain.ErrPermissionWhitelistInvalid, domain.MaxSendWhitelistSize)
	}
	if _, self := slices.BinarySearch(out, selfID); selfID != 0 && self {
		return nil, fmt.Errorf("%w: contains the account itself", domain.ErrPermissionWhitelistInvalid)
	}
	if len(out) == 0 {
		return out, nil
	}
	users, err := reader.ByIDs(ctx, out)
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
