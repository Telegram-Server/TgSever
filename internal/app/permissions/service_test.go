package permissions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type serviceFixture struct {
	users *memory.UserStore
	store *memory.AccountPermissionsStore
	svc   *Service
	seq   int
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	users := memory.NewUserStore()
	st := memory.NewAccountPermissionsStore(users)
	return &serviceFixture{users: users, store: st, svc: NewService(st, users)}
}

func (f *serviceFixture) user(t *testing.T) domain.User {
	t.Helper()
	f.seq++
	u, err := f.users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Staff", Phone: fmt.Sprintf("1555030%04d", f.seq)})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func boolp(v bool) *bool { return &v }

func idsp(ids ...int64) *[]int64 { return &ids }

func TestServiceViewWithoutRecordIsNotEnforced(t *testing.T) {
	f := newServiceFixture(t)
	u := f.user(t)
	view, err := f.svc.View(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Enforced() {
		t.Fatal("view without a record is enforced")
	}
	got, found, err := f.svc.AccountPermissions(context.Background(), u.ID)
	if err != nil || found {
		t.Fatalf("AccountPermissions found=%v err=%v", found, err)
	}
	if want := domain.DefaultAccountPermissions(u.ID); !slices.Equal(got.SendWhitelist, want.SendWhitelist) || got.Flags != want.Flags || got.Enabled {
		t.Fatalf("AccountPermissions without record = %+v, want defaults", got)
	}
}

func TestServiceFirstSetDefaultsUnspecifiedItemsToAllowed(t *testing.T) {
	f := newServiceFixture(t)
	u := f.user(t)
	ctx := context.Background()
	_, _, resolved, err := f.svc.Resolve(ctx, u.ID, Update{Enabled: boolp(true), Flags: FlagsUpdate{Edit: boolp(false)}})
	if err != nil {
		t.Fatal(err)
	}
	p := resolved.Permissions()
	want := domain.DefaultAccountPermissionFlags()
	want.Edit = false
	if !p.Enabled || p.Flags != want || p.SendWhitelistEnabled || len(p.SendWhitelist) != 0 {
		t.Fatalf("resolved = %+v, want enabled with only edit denied", p)
	}
	if _, err := f.svc.Set(ctx, resolved, "cmd-1"); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.View(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enforced() || Decide(view, Action{Kind: ActionEdit}) != DenyEdit {
		t.Fatalf("view after set enforced=%v decide=%q", view.Enforced(), Decide(view, Action{Kind: ActionEdit}))
	}
}

func TestServiceDisablingMasterSwitchKeepsSettings(t *testing.T) {
	f := newServiceFixture(t)
	u, member := f.user(t), f.user(t)
	ctx := context.Background()
	set := func(up Update) domain.AccountPermissions {
		t.Helper()
		_, _, p, err := f.svc.Resolve(ctx, u.ID, up)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.svc.Set(ctx, p, "")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	set(Update{Enabled: boolp(true), Flags: FlagsUpdate{Edit: boolp(false), Send: boolp(false)}, SendWhitelistEnabled: boolp(true), SendWhitelist: idsp(member.ID)})
	off := set(Update{Enabled: boolp(false)})
	if off.Enabled || off.Flags.Edit || off.Flags.Send || !off.SendWhitelistEnabled || !slices.Equal(off.SendWhitelist, []int64{member.ID}) {
		t.Fatalf("after switching off = %+v, want settings kept", off)
	}
	view, err := f.svc.View(ctx, u.ID)
	if err != nil || view.Enforced() {
		t.Fatalf("view after switching off enforced=%v err=%v", view.Enforced(), err)
	}
	on := set(Update{Enabled: boolp(true)})
	if on.Flags.Edit || on.Flags.Send || !slices.Equal(on.SendWhitelist, []int64{member.ID}) {
		t.Fatalf("after switching back on = %+v, want previous settings", on)
	}
}

func TestServiceExemptsBotsAndSystemAccounts(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	bot, err := f.users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Bot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	stored := domain.DefaultAccountPermissions(bot.ID)
	stored.Enabled = true
	stored.Flags.Edit = false
	if _, err := f.store.SetAccountPermissions(ctx, stored); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.View(ctx, bot.ID)
	if err != nil || view.Enforced() {
		t.Fatalf("bot view enforced=%v err=%v", view.Enforced(), err)
	}
	if _, enforced, err := f.svc.EnforcedAccountPermissions(ctx, bot.ID); err != nil || enforced {
		t.Fatalf("bot EnforcedAccountPermissions enforced=%v err=%v", enforced, err)
	}
	system, err := f.svc.View(ctx, domain.OfficialSystemUserID)
	if err != nil || system.Enforced() {
		t.Fatalf("system view enforced=%v err=%v", system.Enforced(), err)
	}
}

func TestServiceSetInvalidatesCachedViewAndOtherInstancesOnInvalidate(t *testing.T) {
	f := newServiceFixture(t)
	other := NewService(f.store, f.users)
	u := f.user(t)
	ctx := context.Background()
	for _, svc := range []*Service{f.svc, other} {
		if view, err := svc.View(ctx, u.ID); err != nil || view.Enforced() {
			t.Fatalf("initial view enforced=%v err=%v", view.Enforced(), err)
		}
	}
	_, _, p, err := f.svc.Resolve(ctx, u.ID, Update{Enabled: boolp(true), Flags: FlagsUpdate{Edit: boolp(false)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Set(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	if view, err := f.svc.View(ctx, u.ID); err != nil || !view.Enforced() {
		t.Fatalf("writer view after set enforced=%v err=%v", view.Enforced(), err)
	}
	// 另一个实例在收到读模型通知之前沿用缓存；通知到达（Invalidate）后读到新设置。
	other.InvalidateAccountPermissions(u.ID)
	if view, err := other.View(ctx, u.ID); err != nil || !view.Enforced() {
		t.Fatalf("other instance view after invalidate enforced=%v err=%v", view.Enforced(), err)
	}
}

func TestServiceWhitelistValidation(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	u, a, b := f.user(t), f.user(t), f.user(t)
	deleted := f.user(t)
	lifecycle := memory.NewAccountLifecycleStore(f.users, nil)
	if _, err := lifecycle.ExecuteAccountDeletion(ctx, deleted.ID, domain.AccountDeletionManual, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]int64, domain.MaxSendWhitelistSize+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	for name, list := range map[string][]int64{
		"missing account": {a.ID, 1 << 60},
		"deleted account": {a.ID, deleted.ID},
		"contains self":   {a.ID, u.ID},
		"over the limit":  tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := f.svc.Resolve(ctx, u.ID, Update{SendWhitelist: &list}); !errors.Is(err, domain.ErrPermissionWhitelistInvalid) {
				t.Fatalf("Resolve err=%v, want ErrPermissionWhitelistInvalid", err)
			}
		})
	}
	_, _, p, err := f.svc.Resolve(ctx, u.ID, Update{SendWhitelist: idsp(b.ID, a.ID, b.ID, a.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{a.ID, b.ID}; !slices.Equal(p.Permissions().SendWhitelist, want) {
		t.Fatalf("deduplicated whitelist = %v, want %v", p.Permissions().SendWhitelist, want)
	}
}
