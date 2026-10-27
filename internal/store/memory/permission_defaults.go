package memory

import (
	"context"
	"slices"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 默认权限模板（#161）的内存实现挂在 AccountPermissionsStore 上：与账号权限共用一把锁，账号注销
// 经 DeleteAccountPermissions 一并从模板白名单中删除。

func (s *AccountPermissionsStore) GetPermissionDefaults(context.Context) (domain.PermissionDefaults, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasDefaults {
		return domain.PermissionDefaults{}, false, nil
	}
	return cloneDefaults(s.defaults), true, nil
}

func (s *AccountPermissionsStore) UpdatePermissionDefaults(ctx context.Context, merge store.PermissionDefaultsMerge) (domain.PermissionDefaults, bool, domain.PermissionDefaults, error) {
	// 与 UpdateAccountPermissions 一样做乐观并发：成员是否存在要查 UserStore（它有自己的锁），
	// 不能在 s.mu 内查。快照在合并、校验期间变了（包括有账号被注销）就重来。
	for {
		s.mu.Lock()
		prev, found, seen := cloneDefaults(s.defaults), s.hasDefaults, s.defaultsWrites
		s.mu.Unlock()
		if !found {
			prev = domain.PermissionDefaults{}
		}

		next := merge(cloneDefaults(prev), found)
		members, err := s.activeMembers(ctx, next.SendWhitelist)
		if err != nil {
			return domain.PermissionDefaults{}, false, domain.PermissionDefaults{}, err
		}
		next.SendWhitelist = members

		s.mu.Lock()
		if s.defaultsWrites == seen {
			next.UpdatedAt = time.Now().UTC()
			s.defaults, s.hasDefaults = cloneDefaults(next), true
			s.defaultsWrites++
			s.mu.Unlock()
			return prev, found, next, nil
		}
		s.mu.Unlock()
	}
}

// activeMembers 去重排序名单并去掉已注销的账号；有账号不存在时返回 domain.ErrUserNotFound。
func (s *AccountPermissionsStore) activeMembers(ctx context.Context, ids []int64) ([]int64, error) {
	ids = domain.NormalizeSendWhitelist(ids)
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		u, found, err := s.users.ByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, domain.ErrUserNotFound
		}
		if !u.Deleted {
			out = append(out, id)
		}
	}
	return out, nil
}

func cloneDefaults(d domain.PermissionDefaults) domain.PermissionDefaults {
	d.SendWhitelist = slices.Clone(d.SendWhitelist)
	if d.SendWhitelist == nil {
		d.SendWhitelist = []int64{}
	}
	return d
}
