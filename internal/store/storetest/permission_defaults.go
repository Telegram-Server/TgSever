package storetest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PermissionDefaultsFixture 是同一份底层数据上的 UserStore 与默认权限模板 store，开始时没有模板。
// NewPhone 每次返回一个还没有人占用的手机号；DeleteAccount 走该实现的账号注销。
type PermissionDefaultsFixture struct {
	Users         store.UserStore
	Defaults      store.PermissionDefaultsStore
	NewPhone      func(t *testing.T) string
	DeleteAccount func(t *testing.T, userID int64)
}

// PermissionDefaultsContract 是默认权限模板的存储契约（#161）：没有模板时读不到、写入后整体
// 读回与整体替换、不存在的成员被拒绝且不写入、并发的合并更新不互相覆盖、账号注销时从模板白名单
// 中级联删除。
func PermissionDefaultsContract(t *testing.T, newFixture func(t *testing.T) PermissionDefaultsFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("no template until the first write", func(t *testing.T) {
		f := newFixture(t)
		if _, found, err := f.Defaults.GetPermissionDefaults(ctx); err != nil || found {
			t.Fatalf("get template before any write found=%v err=%v, want not found", found, err)
		}
	})

	t.Run("update writes then replaces the whole template", func(t *testing.T) {
		f := newFixture(t)
		a, b, c := createDefaultsUser(t, f), createDefaultsUser(t, f), createDefaultsUser(t, f)
		first := domain.PermissionDefaults{
			Enabled: true,
			Flags: domain.AccountPermissionFlags{
				Send: true, Delete: false, Edit: true, ClearHistory: false,
				CreateGroup: true, CreateChannel: false, JoinGroup: false, JoinChannel: true,
				LeaveGroup: true, LeaveChannel: false, AddContact: true,
			},
			SendWhitelistEnabled: true,
			SendWhitelist:        []int64{c.ID, a.ID, c.ID},
			ApplyToSelfSignup:    true,
			CommandID:            "defaults-1",
		}
		prev, found, written, err := f.Defaults.UpdatePermissionDefaults(ctx, func(cur domain.PermissionDefaults, found bool) domain.PermissionDefaults {
			if found {
				t.Errorf("first merge saw a template: %+v", cur)
			}
			return first
		})
		if err != nil || found || !reflect.DeepEqual(prev, domain.PermissionDefaults{}) {
			t.Fatalf("first update prev=%+v found=%v err=%v, want no previous template", prev, found, err)
		}
		want := first
		want.SendWhitelist = sortedIDs(a.ID, c.ID)
		if written.UpdatedAt.IsZero() {
			t.Fatal("written UpdatedAt is zero")
		}
		written.UpdatedAt = want.UpdatedAt
		if !reflect.DeepEqual(written, want) {
			t.Fatalf("written = %+v, want %+v", written, want)
		}
		assertDefaults(t, f, want)

		second := domain.PermissionDefaults{
			Flags:         domain.DefaultAccountPermissionFlags(),
			SendWhitelist: []int64{b.ID},
			CommandID:     "defaults-2",
		}
		prev, found, _, err = f.Defaults.UpdatePermissionDefaults(ctx, func(domain.PermissionDefaults, bool) domain.PermissionDefaults {
			return second
		})
		if err != nil || !found {
			t.Fatalf("second update found=%v err=%v", found, err)
		}
		prev.UpdatedAt = want.UpdatedAt
		if !reflect.DeepEqual(prev, want) {
			t.Fatalf("second update prev = %+v, want %+v", prev, want)
		}
		assertDefaults(t, f, second)

		cleared := second
		cleared.SendWhitelist = nil
		cleared.CommandID = "defaults-3"
		if _, _, _, err := f.Defaults.UpdatePermissionDefaults(ctx, func(domain.PermissionDefaults, bool) domain.PermissionDefaults {
			return cleared
		}); err != nil {
			t.Fatalf("clear whitelist: %v", err)
		}
		assertDefaults(t, f, cleared)
	})

	t.Run("missing whitelist members are rejected without writing", func(t *testing.T) {
		f := newFixture(t)
		member := createDefaultsUser(t, f)
		seed := domain.PermissionDefaults{Enabled: true, Flags: domain.DefaultAccountPermissionFlags(), SendWhitelistEnabled: true, SendWhitelist: []int64{member.ID}}
		if _, _, _, err := f.Defaults.UpdatePermissionDefaults(ctx, func(domain.PermissionDefaults, bool) domain.PermissionDefaults { return seed }); err != nil {
			t.Fatalf("seed template: %v", err)
		}
		const missingUserID = int64(1) << 60
		if _, _, _, err := f.Defaults.UpdatePermissionDefaults(ctx, func(cur domain.PermissionDefaults, _ bool) domain.PermissionDefaults {
			cur.Enabled = false
			cur.SendWhitelist = append(cur.SendWhitelist, missingUserID)
			return cur
		}); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("update with a missing member err=%v, want ErrUserNotFound", err)
		}
		assertDefaults(t, f, seed)
	})

	t.Run("update merges under a lock so concurrent partial updates all survive", func(t *testing.T) {
		f := newFixture(t)
		turnOff := []func(*domain.AccountPermissionFlags){
			func(p *domain.AccountPermissionFlags) { p.Send = false },
			func(p *domain.AccountPermissionFlags) { p.Delete = false },
			func(p *domain.AccountPermissionFlags) { p.Edit = false },
			func(p *domain.AccountPermissionFlags) { p.ClearHistory = false },
			func(p *domain.AccountPermissionFlags) { p.CreateGroup = false },
			func(p *domain.AccountPermissionFlags) { p.CreateChannel = false },
			func(p *domain.AccountPermissionFlags) { p.JoinGroup = false },
			func(p *domain.AccountPermissionFlags) { p.JoinChannel = false },
			func(p *domain.AccountPermissionFlags) { p.LeaveGroup = false },
			func(p *domain.AccountPermissionFlags) { p.LeaveChannel = false },
			func(p *domain.AccountPermissionFlags) { p.AddContact = false },
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		firsts := 0
		errs := make(chan error, len(turnOff))
		for _, off := range turnOff {
			wg.Add(1)
			go func(off func(*domain.AccountPermissionFlags)) {
				defer wg.Done()
				_, found, _, err := f.Defaults.UpdatePermissionDefaults(ctx, func(cur domain.PermissionDefaults, found bool) domain.PermissionDefaults {
					if !found {
						cur = domain.DefaultPermissionDefaults()
						cur.Enabled = true
					}
					off(&cur.Flags)
					return cur
				})
				if err == nil && !found {
					mu.Lock()
					firsts++
					mu.Unlock()
				}
				errs <- err
			}(off)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent update: %v", err)
			}
		}
		if firsts != 1 {
			t.Fatalf("%d updates saw no template, want exactly the first one", firsts)
		}
		got, found, err := f.Defaults.GetPermissionDefaults(ctx)
		if err != nil || !found || !got.Enabled || got.Flags != (domain.AccountPermissionFlags{}) {
			t.Fatalf("after 11 concurrent partial updates = %+v found=%v err=%v, want enabled with every item off", got, found, err)
		}
	})

	t.Run("account deletion removes the account from the template whitelist", func(t *testing.T) {
		f := newFixture(t)
		gone, kept := createDefaultsUser(t, f), createDefaultsUser(t, f)
		seed := domain.PermissionDefaults{
			Enabled: true, Flags: domain.DefaultAccountPermissionFlags(),
			SendWhitelistEnabled: true, SendWhitelist: sortedIDs(gone.ID, kept.ID), ApplyToSelfSignup: true, CommandID: "defaults-seed",
		}
		if _, _, _, err := f.Defaults.UpdatePermissionDefaults(ctx, func(domain.PermissionDefaults, bool) domain.PermissionDefaults { return seed }); err != nil {
			t.Fatalf("seed template: %v", err)
		}
		f.DeleteAccount(t, gone.ID)
		seed.SendWhitelist = []int64{kept.ID}
		assertDefaults(t, f, seed)

		// 已注销的账号写进模板时被排除，不会留下指向它的条目。
		if _, _, _, err := f.Defaults.UpdatePermissionDefaults(ctx, func(cur domain.PermissionDefaults, _ bool) domain.PermissionDefaults {
			cur.SendWhitelist = append(cur.SendWhitelist, gone.ID)
			return cur
		}); err != nil {
			t.Fatalf("update with a deleted member: %v", err)
		}
		assertDefaults(t, f, seed)
	})
}

func createDefaultsUser(t *testing.T, f PermissionDefaultsFixture) domain.User {
	t.Helper()
	created, err := f.Users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Lead", Phone: f.NewPhone(t)})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

// assertDefaults 比较除 UpdatedAt 之外的全部字段；nil 与空白名单等价于 []int64{}。
func assertDefaults(t *testing.T, f PermissionDefaultsFixture, want domain.PermissionDefaults) {
	t.Helper()
	got, found, err := f.Defaults.GetPermissionDefaults(context.Background())
	if err != nil || !found {
		t.Fatalf("get template found=%v err=%v", found, err)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("stored template UpdatedAt is zero")
	}
	got.UpdatedAt = want.UpdatedAt
	if got.SendWhitelist == nil {
		got.SendWhitelist = []int64{}
	}
	if want.SendWhitelist == nil {
		want.SendWhitelist = []int64{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %+v, want %+v", got, want)
	}
}
