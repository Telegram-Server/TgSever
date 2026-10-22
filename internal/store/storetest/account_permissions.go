package storetest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AccountPermissionsFixture 是同一份底层数据上的 UserStore 与权限设置 store。NewPhone 每次
// 返回一个还没有人占用的手机号；DeleteAccount 走该实现的账号注销。
type AccountPermissionsFixture struct {
	Users         store.UserStore
	Permissions   store.AccountPermissionsStore
	NewPhone      func(t *testing.T) string
	DeleteAccount func(t *testing.T, userID int64)
}

// AccountPermissionsContract 是员工权限设置的存储契约（#119）：读取不存在的记录、设置与整体
// 替换（含白名单）、不存在的账号被拒绝且不写入、并发提交名单不混合、账号注销级联删除。
func AccountPermissionsContract(t *testing.T, newFixture func(t *testing.T) AccountPermissionsFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("missing record is not found", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		if _, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID); err != nil || found {
			t.Fatalf("get missing permissions found=%v err=%v, want not found", found, err)
		}
	})

	t.Run("set then replace including the whitelist", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		a, b, c := createPermissionsUser(t, f), createPermissionsUser(t, f), createPermissionsUser(t, f)
		first := domain.AccountPermissions{
			UserID:  u.ID,
			Enabled: true,
			Flags: domain.AccountPermissionFlags{
				Send: true, Edit: false, Delete: false, ClearHistory: true,
				CreateGroup: false, CreateChannel: true, JoinGroup: true, JoinChannel: false,
				LeaveGroup: false, LeaveChannel: true, AddContact: false,
			},
			SendWhitelistEnabled: true,
			SendWhitelist:        []int64{c.ID, a.ID, a.ID},
			CommandID:            "cmd-1",
		}
		written, err := f.Permissions.SetAccountPermissions(ctx, first)
		if err != nil {
			t.Fatalf("set permissions: %v", err)
		}
		if written.UpdatedAt.IsZero() {
			t.Fatal("set permissions UpdatedAt is zero")
		}
		want := first
		want.SendWhitelist = sortedIDs(a.ID, c.ID)
		assertPermissions(t, f, want)

		second := domain.AccountPermissions{
			UserID:               u.ID,
			Enabled:              false,
			Flags:                first.Flags,
			SendWhitelistEnabled: false,
			SendWhitelist:        []int64{b.ID},
			CommandID:            "cmd-2",
		}
		if _, err := f.Permissions.SetAccountPermissions(ctx, second); err != nil {
			t.Fatalf("replace permissions: %v", err)
		}
		assertPermissions(t, f, second)

		empty := second
		empty.SendWhitelist = nil
		empty.CommandID = "cmd-3"
		if _, err := f.Permissions.SetAccountPermissions(ctx, empty); err != nil {
			t.Fatalf("clear whitelist: %v", err)
		}
		empty.SendWhitelist = []int64{}
		assertPermissions(t, f, empty)
	})

	t.Run("missing accounts are rejected without writing", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		const missingUserID = int64(1) << 60
		if _, err := f.Permissions.SetAccountPermissions(ctx, domain.AccountPermissions{UserID: missingUserID, Enabled: true}); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("set on missing user err=%v, want ErrUserNotFound", err)
		}
		member := createPermissionsUser(t, f)
		if _, err := f.Permissions.SetAccountPermissions(ctx, domain.AccountPermissions{
			UserID: u.ID, Enabled: true, SendWhitelistEnabled: true, SendWhitelist: []int64{member.ID, missingUserID},
		}); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("set with missing whitelist member err=%v, want ErrUserNotFound", err)
		}
		if _, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID); err != nil || found {
			t.Fatalf("after rejected set found=%v err=%v, want nothing written", found, err)
		}
	})

	t.Run("concurrent submissions never mix whitelists", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		const writers = 6
		const perWriter = 5
		lists := make([][]int64, writers)
		for i := range lists {
			for j := 0; j < perWriter; j++ {
				lists[i] = append(lists[i], createPermissionsUser(t, f).ID)
			}
			slices.Sort(lists[i])
		}
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(list []int64) {
				defer wg.Done()
				_, err := f.Permissions.SetAccountPermissions(ctx, domain.AccountPermissions{
					UserID: u.ID, Enabled: true, Flags: domain.DefaultAccountPermissionFlags(),
					SendWhitelistEnabled: true, SendWhitelist: list,
				})
				errs <- err
			}(lists[i])
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent set: %v", err)
			}
		}
		got, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID)
		if err != nil || !found {
			t.Fatalf("get after concurrent sets found=%v err=%v", found, err)
		}
		for _, list := range lists {
			if slices.Equal(got.SendWhitelist, list) {
				return
			}
		}
		t.Fatalf("whitelist after concurrent sets = %v, want exactly one submitted list of %v", got.SendWhitelist, lists)
	})

	t.Run("a read never pairs one submission's flags with another's whitelist", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		a, b := createPermissionsUser(t, f), createPermissionsUser(t, f)
		// 两份提交的总开关与名单一一对应：读到 enabled=true 就必须是 [a]，false 就必须是 [b]。
		states := []domain.AccountPermissions{
			{UserID: u.ID, Enabled: true, Flags: domain.DefaultAccountPermissionFlags(), SendWhitelistEnabled: true, SendWhitelist: []int64{a.ID}},
			{UserID: u.ID, Enabled: false, Flags: domain.DefaultAccountPermissionFlags(), SendWhitelistEnabled: true, SendWhitelist: []int64{b.ID}},
		}
		if _, err := f.Permissions.SetAccountPermissions(ctx, states[0]); err != nil {
			t.Fatalf("seed permissions: %v", err)
		}
		done := make(chan struct{})
		writeErr := make(chan error, 1)
		go func() {
			defer close(done)
			for i := 0; i < 200; i++ {
				if _, err := f.Permissions.SetAccountPermissions(ctx, states[i%2]); err != nil {
					writeErr <- err
					return
				}
			}
		}()
		for reading := true; reading; {
			select {
			case <-done:
				reading = false
			default:
			}
			got, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID)
			if err != nil || !found {
				t.Fatalf("read during writes found=%v err=%v", found, err)
			}
			want := states[1].SendWhitelist
			if got.Enabled {
				want = states[0].SendWhitelist
			}
			if !slices.Equal(got.SendWhitelist, want) {
				t.Fatalf("read enabled=%v with whitelist %v, want %v: flags and whitelist come from different submissions", got.Enabled, got.SendWhitelist, want)
			}
		}
		select {
		case err := <-writeErr:
			t.Fatalf("write during reads: %v", err)
		default:
		}
	})

	t.Run("update merges under a lock so concurrent partial updates all survive", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		// 11 个并发的部分更新各自只关掉一项：每次合并都必须基于前一次写入后的值，否则后提交的
		// 会把先提交的那一项改回允许。
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
				_, found, _, err := f.Permissions.UpdateAccountPermissions(ctx, u.ID, func(cur domain.AccountPermissions, found bool) domain.AccountPermissions {
					if !found {
						cur = domain.AccountPermissions{Enabled: true, Flags: domain.DefaultAccountPermissionFlags()}
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
			t.Fatalf("%d updates saw no record, want exactly the first one", firsts)
		}
		got, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID)
		if err != nil || !found {
			t.Fatalf("get after concurrent updates found=%v err=%v", found, err)
		}
		if got.Flags != (domain.AccountPermissionFlags{}) || !got.Enabled {
			t.Fatalf("after 11 concurrent partial updates enabled=%v flags=%+v, want enabled with every item off", got.Enabled, got.Flags)
		}
	})

	t.Run("update returns the previous value and rejects missing accounts", func(t *testing.T) {
		f := newFixture(t)
		u := createPermissionsUser(t, f)
		a := createPermissionsUser(t, f)
		seed := domain.AccountPermissions{UserID: u.ID, Enabled: true, Flags: domain.DefaultAccountPermissionFlags(), SendWhitelistEnabled: true, SendWhitelist: []int64{a.ID}}
		if _, err := f.Permissions.SetAccountPermissions(ctx, seed); err != nil {
			t.Fatalf("seed permissions: %v", err)
		}
		prev, found, written, err := f.Permissions.UpdateAccountPermissions(ctx, u.ID, func(cur domain.AccountPermissions, _ bool) domain.AccountPermissions {
			cur.Enabled = false
			cur.CommandID = "cmd-update"
			return cur
		})
		if err != nil || !found || !prev.Enabled || !slices.Equal(prev.SendWhitelist, []int64{a.ID}) {
			t.Fatalf("update prev=%+v found=%v err=%v, want the seeded record", prev, found, err)
		}
		if written.Enabled || written.CommandID != "cmd-update" || !slices.Equal(written.SendWhitelist, []int64{a.ID}) || written.UpdatedAt.IsZero() {
			t.Fatalf("update wrote %+v, want enabled off with the whitelist and command id kept", written)
		}
		assertPermissions(t, f, written)

		if _, _, _, err := f.Permissions.UpdateAccountPermissions(ctx, 1<<40, func(cur domain.AccountPermissions, _ bool) domain.AccountPermissions {
			return cur
		}); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("update of a missing account err=%v, want ErrUserNotFound", err)
		}
		if _, _, _, err := f.Permissions.UpdateAccountPermissions(ctx, u.ID, func(cur domain.AccountPermissions, _ bool) domain.AccountPermissions {
			cur.SendWhitelist = append(cur.SendWhitelist, 1<<40)
			return cur
		}); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("update with a missing whitelist account err=%v, want ErrUserNotFound", err)
		}
		assertPermissions(t, f, written)
	})

	t.Run("account deletion removes its permissions and its whitelist entries elsewhere", func(t *testing.T) {
		f := newFixture(t)
		owner := createPermissionsUser(t, f)
		other := createPermissionsUser(t, f)
		kept := createPermissionsUser(t, f)
		if _, err := f.Permissions.SetAccountPermissions(ctx, domain.AccountPermissions{
			UserID: owner.ID, Enabled: true, SendWhitelistEnabled: true, SendWhitelist: []int64{kept.ID},
		}); err != nil {
			t.Fatalf("set owner permissions: %v", err)
		}
		otherPerms := domain.AccountPermissions{
			UserID: other.ID, Enabled: true, Flags: domain.DefaultAccountPermissionFlags(),
			SendWhitelistEnabled: true, SendWhitelist: []int64{owner.ID, kept.ID},
		}
		if _, err := f.Permissions.SetAccountPermissions(ctx, otherPerms); err != nil {
			t.Fatalf("set other permissions: %v", err)
		}
		f.DeleteAccount(t, owner.ID)
		if _, found, err := f.Permissions.GetAccountPermissions(ctx, owner.ID); err != nil || found {
			t.Fatalf("deleted account permissions found=%v err=%v, want gone", found, err)
		}
		otherPerms.SendWhitelist = []int64{kept.ID}
		assertPermissions(t, f, otherPerms)
	})
}

func createPermissionsUser(t *testing.T, f AccountPermissionsFixture) domain.User {
	t.Helper()
	created, err := f.Users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Staff", Phone: f.NewPhone(t)})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

func sortedIDs(ids ...int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// assertPermissions 比较除 UpdatedAt 之外的全部字段；nil 与空白名单等价于 []int64{}。
func assertPermissions(t *testing.T, f AccountPermissionsFixture, want domain.AccountPermissions) {
	t.Helper()
	got, found, err := f.Permissions.GetAccountPermissions(context.Background(), want.UserID)
	if err != nil || !found {
		t.Fatalf("get permissions found=%v err=%v", found, err)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("stored permissions UpdatedAt is zero")
	}
	got.UpdatedAt = want.UpdatedAt
	if got.SendWhitelist == nil {
		got.SendWhitelist = []int64{}
	}
	if want.SendWhitelist == nil {
		want.SendWhitelist = []int64{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %+v, want %+v", got, want)
	}
}
