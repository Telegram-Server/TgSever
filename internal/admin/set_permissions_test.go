package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"telesrv/internal/app/permissions"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台设置员工权限命令（account.set_permissions，#119）的命令层测试：内存的用户与权限 store，
// 命令通道（审计、试运行、按 command id 幂等与冲突检测）用 memoryCommandRepo。

type permissionsNotifierSpy struct {
	userIDs []int64
	err     error
}

func (n *permissionsNotifierSpy) NotifyAccountPermissionsChanged(_ context.Context, userID int64) error {
	n.userIDs = append(n.userIDs, userID)
	return n.err
}

type permissionsFixture struct {
	svc      *Service
	repo     *memoryCommandRepo
	users    *memory.UserStore
	perms    *permissions.Service
	notifier *permissionsNotifierSpy
	seq      int
}

func newPermissionsFixture(t *testing.T) *permissionsFixture {
	t.Helper()
	f := &permissionsFixture{repo: newMemoryCommandRepo(), users: memory.NewUserStore(), notifier: &permissionsNotifierSpy{}}
	f.perms = permissions.NewService(memory.NewAccountPermissionsStore(f.users), f.users)
	f.svc = NewService(Dependencies{
		Commands:            f.repo,
		Users:               users.NewService(f.users),
		Permissions:         f.perms,
		PermissionsNotifier: f.notifier,
		Now:                 fixedNow,
	})
	return f
}

func (f *permissionsFixture) user(t *testing.T, u domain.User) domain.User {
	t.Helper()
	if u.Phone == "" && !u.Bot {
		f.seq++
		u.Phone = fmt.Sprintf("1555040%04d", f.seq)
	}
	if u.FirstName == "" {
		u.FirstName = "Staff"
	}
	created, err := f.users.Create(context.Background(), u)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

func (f *permissionsFixture) stored(t *testing.T, userID int64) (domain.AccountPermissions, bool) {
	t.Helper()
	p, found, err := f.perms.AccountPermissions(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return p, found
}

func permissionsRequest(commandID string, userID int64) SetPermissionsRequest {
	return SetPermissionsRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "staff account"},
		UserID:      userID,
	}
}

func bptr(v bool) *bool { return &v }

func TestSetPermissionsSetsReplacesAndNotifies(t *testing.T) {
	f := newPermissionsFixture(t)
	ctx := context.Background()
	u, a, b := f.user(t, domain.User{}), f.user(t, domain.User{}), f.user(t, domain.User{})

	req := permissionsRequest("exec-permissions-1", u.ID)
	req.Enabled = bptr(true)
	req.Edit = bptr(false)
	req.SendWhitelist = &PermissionsWhitelistRequest{Enabled: bptr(true), UserIDs: &[]int64{b.ID, a.ID, b.ID}}
	res, err := f.svc.SetPermissions(ctx, req)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionSetPermissions || res.TargetUserID != u.ID || res.Details["would_change"] != true {
		t.Fatalf("set result = %+v", res)
	}
	got, found := f.stored(t, u.ID)
	wantFlags := domain.DefaultAccountPermissionFlags()
	wantFlags.Edit = false
	if !found || !got.Enabled || got.Flags != wantFlags || !got.SendWhitelistEnabled || !slices.Equal(got.SendWhitelist, []int64{a.ID, b.ID}) || got.CommandID != "exec-permissions-1" {
		t.Fatalf("stored = %+v found=%v", got, found)
	}
	if !slices.Equal(f.notifier.userIDs, []int64{u.ID}) {
		t.Fatalf("notified = %v, want [%d]", f.notifier.userIDs, u.ID)
	}

	off := permissionsRequest("exec-permissions-2", u.ID)
	off.Enabled = bptr(false)
	if _, err := f.svc.SetPermissions(ctx, off); err != nil {
		t.Fatalf("switch off: %v", err)
	}
	got, _ = f.stored(t, u.ID)
	if got.Enabled || got.Flags != wantFlags || !slices.Equal(got.SendWhitelist, []int64{a.ID, b.ID}) {
		t.Fatalf("after switch off = %+v, want settings kept", got)
	}
	if !slices.Equal(f.notifier.userIDs, []int64{u.ID, u.ID}) {
		t.Fatalf("switching off did not notify: %v", f.notifier.userIDs)
	}
	view, err := f.perms.View(ctx, u.ID)
	if err != nil || view.Enforced() {
		t.Fatalf("view after switch off enforced=%v err=%v", view.Enforced(), err)
	}

	result, err := f.svc.AccountPermissions(ctx, u.ID)
	if err != nil || !result.Found || result.Enabled || result.Edit || !result.Send || !slices.Equal(result.SendWhitelist.UserIDs, []int64{a.ID, b.ID}) {
		t.Fatalf("AccountPermissions = %+v, %v", result, err)
	}
	if f.repo.items["exec-permissions-2"].Action != ActionSetPermissions || f.repo.items["exec-permissions-2"].Actor != "ops" {
		t.Fatalf("command not recorded: %+v", f.repo.items["exec-permissions-2"])
	}
}

func TestAccountPermissionsWithoutRecordReturnsDefaults(t *testing.T) {
	f := newPermissionsFixture(t)
	u := f.user(t, domain.User{})
	result, err := f.svc.AccountPermissions(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Found || result.Enabled || !result.Send || !result.Edit || !result.AddContact || result.SendWhitelist.Enabled || result.SendWhitelist.UserIDs == nil || len(result.SendWhitelist.UserIDs) != 0 {
		t.Fatalf("defaults = %+v", result)
	}
}

func TestSetPermissionsDryRunWritesNothing(t *testing.T) {
	f := newPermissionsFixture(t)
	u := f.user(t, domain.User{})
	req := permissionsRequest("dry-permissions", u.ID)
	req.Enabled = bptr(true)
	req.DryRun = true
	res, err := f.svc.SetPermissions(context.Background(), req)
	if err != nil || !res.DryRun || res.Details["would_change"] != true {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if _, found := f.stored(t, u.ID); found {
		t.Fatal("dry run wrote permissions")
	}
	if len(f.notifier.userIDs) != 0 {
		t.Fatalf("dry run notified %v", f.notifier.userIDs)
	}
	if !f.repo.items["dry-permissions"].DryRun {
		t.Fatalf("dry run not recorded: %+v", f.repo.items["dry-permissions"])
	}
}

func TestSetPermissionsIsIdempotentPerCommandIDAndConflictsOnAnotherValue(t *testing.T) {
	f := newPermissionsFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{})
	req := permissionsRequest("exec-permissions-replay", u.ID)
	req.Enabled = bptr(true)
	req.Edit = bptr(false)
	if _, err := f.svc.SetPermissions(ctx, req); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.SetPermissions(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if len(f.notifier.userIDs) != 1 {
		t.Fatalf("replay notified again: %v", f.notifier.userIDs)
	}
	other := req
	other.Edit = bptr(true)
	if conflict, err := f.svc.SetPermissions(ctx, other); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict = %+v, %v", conflict, err)
	}
	if got, _ := f.stored(t, u.ID); got.Flags.Edit {
		t.Fatal("conflicting replay changed the stored permissions")
	}
}

func TestSetPermissionsRejectsSystemBotDeletedAndMissingAccounts(t *testing.T) {
	f := newPermissionsFixture(t)
	bot := f.user(t, domain.User{FirstName: "Bot", Bot: true, Username: "perm_bot"})
	deleted := f.user(t, domain.User{Deleted: true})
	for _, tc := range []struct {
		name   string
		userID int64
	}{
		{"system", domain.OfficialSystemUserID},
		{"bot", bot.ID},
		{"deleted", deleted.ID},
		{"missing", 987654321},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dry := range []bool{true, false} {
				req := permissionsRequest(fmt.Sprintf("reject-permissions-%s-%v", tc.name, dry), tc.userID)
				req.Enabled = bptr(true)
				req.DryRun = dry
				res, err := f.svc.SetPermissions(context.Background(), req)
				if err == nil || res.Code != "USER_INVALID" {
					t.Fatalf("dry=%v result = %+v, %v; want USER_INVALID", dry, res, err)
				}
				if f.repo.items[req.CommandID].Status != domain.AdminCommandFailed {
					t.Fatalf("rejection not recorded as failed: %+v", f.repo.items[req.CommandID])
				}
			}
			if _, found := f.stored(t, tc.userID); found {
				t.Fatal("rejected target has permissions")
			}
		})
	}
}

func TestSetPermissionsRejectsInvalidWhitelistBeforeTheCommandChannel(t *testing.T) {
	f := newPermissionsFixture(t)
	u, a := f.user(t, domain.User{}), f.user(t, domain.User{})
	deleted := f.user(t, domain.User{Deleted: true})
	tooMany := make([]int64, domain.MaxSendWhitelistSize+1)
	for i := range tooMany {
		tooMany[i] = a.ID + int64(i)
	}
	for name, ids := range map[string][]int64{
		"missing":  {a.ID, 987654321},
		"deleted":  {deleted.ID},
		"self":     {u.ID},
		"too many": tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			req := permissionsRequest("bad-whitelist-"+name, u.ID)
			req.SendWhitelist = &PermissionsWhitelistRequest{Enabled: bptr(true), UserIDs: &ids}
			_, err := f.svc.SetPermissions(context.Background(), req)
			if ErrorCode(err) != "PERMISSION_WHITELIST_INVALID" || !errors.Is(err, domain.ErrPermissionWhitelistInvalid) {
				t.Fatalf("err = %v (code %q), want PERMISSION_WHITELIST_INVALID", err, ErrorCode(err))
			}
			if _, recorded := f.repo.items[req.CommandID]; recorded {
				t.Fatal("invalid whitelist was recorded as a command")
			}
		})
	}
}

func TestSetPermissionsRecordsNotifyFailureWithoutFailing(t *testing.T) {
	f := newPermissionsFixture(t)
	f.notifier.err = errors.New("router down")
	u := f.user(t, domain.User{})
	req := permissionsRequest("notify-fails", u.ID)
	req.Enabled = bptr(true)
	res, err := f.svc.SetPermissions(context.Background(), req)
	if err != nil || res.Details["notify_error"] != "router down" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if _, found := f.stored(t, u.ID); !found {
		t.Fatal("permissions not written")
	}
}
