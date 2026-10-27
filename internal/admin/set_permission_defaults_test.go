package admin

import (
	"context"
	"errors"
	"slices"
	"testing"

	"telesrv/internal/app/permissions"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台设置默认权限模板（permission_defaults.set，#161）的命令层测试：内存的用户与模板 store，
// 命令通道用 memoryCommandRepo。

type defaultsFixture struct {
	*permissionsFixture
	defaults *permissions.DefaultsService
}

func newDefaultsFixture(t *testing.T) *defaultsFixture {
	t.Helper()
	f := &defaultsFixture{permissionsFixture: &permissionsFixture{repo: newMemoryCommandRepo(), users: memory.NewUserStore()}}
	f.defaults = permissions.NewDefaultsService(memory.NewAccountPermissionsStore(f.users), f.users)
	f.svc = NewService(Dependencies{
		Commands:           f.repo,
		Users:              users.NewService(f.users),
		PermissionDefaults: f.defaults,
		Now:                fixedNow,
	})
	return f
}

func (f *defaultsFixture) storedDefaults(t *testing.T) (domain.PermissionDefaults, bool) {
	t.Helper()
	d, found, err := f.defaults.PermissionDefaults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d, found
}

func defaultsRequest(commandID string) SetPermissionDefaultsRequest {
	return SetPermissionDefaultsRequest{CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "staff defaults"}}
}

func TestPermissionDefaultsWithoutTemplateReturnsUncontrolledDefaults(t *testing.T) {
	f := newDefaultsFixture(t)
	result, err := f.svc.PermissionDefaults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Found || result.Enabled || !result.Send || !result.AddContact || result.ApplyToSelfSignup ||
		result.SendWhitelist.Enabled || result.SendWhitelist.UserIDs == nil || len(result.SendWhitelist.UserIDs) != 0 {
		t.Fatalf("defaults without a template = %+v", result)
	}
}

func TestSetPermissionDefaultsWritesThenMergesGivenFields(t *testing.T) {
	f := newDefaultsFixture(t)
	ctx := context.Background()
	a, b := f.user(t, domain.User{}), f.user(t, domain.User{})

	req := defaultsRequest("exec-defaults-1")
	req.Enabled = bptr(true)
	req.Edit = bptr(false)
	req.ApplyToSelfSignup = bptr(true)
	req.SendWhitelist = &PermissionsWhitelistRequest{Enabled: bptr(true), UserIDs: &[]int64{b.ID, a.ID, b.ID}}
	res, err := f.svc.SetPermissionDefaults(ctx, req)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionSetPermissionDefaults || res.Details["would_change"] != true {
		t.Fatalf("set result = %+v", res)
	}
	got, found := f.storedDefaults(t)
	wantFlags := domain.DefaultAccountPermissionFlags()
	wantFlags.Edit = false
	if !found || !got.Enabled || got.Flags != wantFlags || !got.ApplyToSelfSignup || !got.SendWhitelistEnabled ||
		!slices.Equal(got.SendWhitelist, []int64{a.ID, b.ID}) || got.CommandID != "exec-defaults-1" {
		t.Fatalf("stored = %+v found=%v", got, found)
	}

	// 只给出的字段被替换：关掉自助注册，其余沿用。
	off := defaultsRequest("exec-defaults-2")
	off.ApplyToSelfSignup = bptr(false)
	if _, err := f.svc.SetPermissionDefaults(ctx, off); err != nil {
		t.Fatalf("switch off self signup: %v", err)
	}
	got, _ = f.storedDefaults(t)
	if got.ApplyToSelfSignup || !got.Enabled || got.Flags != wantFlags || !slices.Equal(got.SendWhitelist, []int64{a.ID, b.ID}) {
		t.Fatalf("after partial update = %+v, want only apply_to_self_signup changed", got)
	}

	result, err := f.svc.PermissionDefaults(ctx)
	if err != nil || !result.Found || !result.Enabled || result.Edit || result.ApplyToSelfSignup || !slices.Equal(result.SendWhitelist.UserIDs, []int64{a.ID, b.ID}) || result.CommandID != "exec-defaults-2" {
		t.Fatalf("PermissionDefaults = %+v, %v", result, err)
	}
	if cmd := f.repo.items["exec-defaults-2"]; cmd.Action != ActionSetPermissionDefaults || cmd.Actor != "ops" || cmd.TargetUserID != 0 {
		t.Fatalf("command not recorded: %+v", cmd)
	}
}

func TestSetPermissionDefaultsDryRunWritesNothing(t *testing.T) {
	f := newDefaultsFixture(t)
	req := defaultsRequest("dry-defaults")
	req.Enabled = bptr(true)
	req.DryRun = true
	res, err := f.svc.SetPermissionDefaults(context.Background(), req)
	if err != nil || !res.DryRun || res.Details["would_change"] != true {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if _, found := f.storedDefaults(t); found {
		t.Fatal("dry run wrote the template")
	}
	if !f.repo.items["dry-defaults"].DryRun {
		t.Fatalf("dry run not recorded: %+v", f.repo.items["dry-defaults"])
	}
}

func TestSetPermissionDefaultsIsIdempotentPerCommandIDAndConflictsOnAnotherValue(t *testing.T) {
	f := newDefaultsFixture(t)
	ctx := context.Background()
	req := defaultsRequest("exec-defaults-replay")
	req.Enabled = bptr(true)
	req.Edit = bptr(false)
	if _, err := f.svc.SetPermissionDefaults(ctx, req); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.SetPermissionDefaults(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	other := req
	other.Edit = bptr(true)
	if conflict, err := f.svc.SetPermissionDefaults(ctx, other); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict = %+v, %v", conflict, err)
	}
	if got, _ := f.storedDefaults(t); got.Flags.Edit {
		t.Fatal("conflicting replay changed the stored template")
	}
}

func TestSetPermissionDefaultsRejectsInvalidWhitelistBeforeTheCommandChannel(t *testing.T) {
	f := newDefaultsFixture(t)
	a := f.user(t, domain.User{})
	deleted := f.user(t, domain.User{Deleted: true})
	tooMany := make([]int64, domain.MaxSendWhitelistSize+1)
	for i := range tooMany {
		tooMany[i] = a.ID + int64(i)
	}
	for name, ids := range map[string][]int64{
		"missing":  {a.ID, 987654321},
		"deleted":  {deleted.ID},
		"too many": tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			for _, dry := range []bool{true, false} {
				req := defaultsRequest("bad-defaults-" + name)
				req.DryRun = dry
				req.SendWhitelist = &PermissionsWhitelistRequest{Enabled: bptr(true), UserIDs: &ids}
				_, err := f.svc.SetPermissionDefaults(context.Background(), req)
				if ErrorCode(err) != "PERMISSION_WHITELIST_INVALID" || !errors.Is(err, domain.ErrPermissionWhitelistInvalid) {
					t.Fatalf("dry=%v err = %v (code %q), want PERMISSION_WHITELIST_INVALID", dry, err, ErrorCode(err))
				}
				if _, recorded := f.repo.items[req.CommandID]; recorded {
					t.Fatal("invalid whitelist was recorded as a command")
				}
			}
			if _, found := f.storedDefaults(t); found {
				t.Fatal("invalid whitelist wrote a template")
			}
		})
	}
}
