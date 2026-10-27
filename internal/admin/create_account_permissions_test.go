package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	authapp "telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/app/permissions"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台建号套用默认权限（account.create 的可选 permissions，#162）的命令层测试。建号用真实的
// auth 服务，权限与模板用真实的 app/permissions 服务，原子建号用内存的 AccountCreationStore，
// 几者共用同一套内存存储。

type createAccountPermissionsFixture struct {
	*permissionsFixture
	defaults *permissions.DefaultsService
}

func newCreateAccountPermissionsFixture(t *testing.T) *createAccountPermissionsFixture {
	t.Helper()
	f := &createAccountPermissionsFixture{permissionsFixture: &permissionsFixture{repo: newMemoryCommandRepo(), users: memory.NewUserStore()}}
	permStore := memory.NewAccountPermissionsStore(f.users)
	f.perms = permissions.NewService(permStore, f.users)
	f.defaults = permissions.NewDefaultsService(permStore, f.users)
	f.svc = NewService(Dependencies{
		Commands:           f.repo,
		Users:              users.NewService(f.users),
		AccountCreator:     authapp.NewService(f.users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345"),
		AccountCreation:    memory.NewAccountCreationStore(f.users, nil, permStore),
		Permissions:        f.perms,
		PermissionDefaults: f.defaults,
		Now:                fixedNow,
	})
	return f
}

// setDefaults 写入一份模板：总开关打开、禁止建群、白名单打开且只含 whitelist。
func (f *createAccountPermissionsFixture) setDefaults(t *testing.T, whitelist ...int64) {
	t.Helper()
	req := defaultsRequest("exec-defaults-for-create")
	req.Enabled = boolPtr(true)
	req.CreateGroup = boolPtr(false)
	req.SendWhitelist = &PermissionsWhitelistRequest{Enabled: boolPtr(true), UserIDs: &whitelist}
	if _, err := f.svc.SetPermissionDefaults(context.Background(), req); err != nil {
		t.Fatalf("SetPermissionDefaults: %v", err)
	}
}

func createAccountRequest(commandID, phone string) CreateAccountRequest {
	return CreateAccountRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "new hire"},
		Phone:       phone,
		FirstName:   "Pat",
	}
}

func (f *createAccountPermissionsFixture) createdID(t *testing.T, res CommandResult) int64 {
	t.Helper()
	id, ok := res.Details["user_id"].(int64)
	if !ok || id <= 0 {
		t.Fatalf("details = %+v, want a user_id", res.Details)
	}
	return id
}

func TestCreateAccountWritesGivenPermissions(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()
	colleague := f.user(t, domain.User{})
	f.setDefaults(t) // 给了 permissions 就不看模板

	whitelist := []int64{colleague.ID, colleague.ID}
	req := createAccountRequest("exec-create-perm-1", "15550170001")
	req.Permissions = &CreateAccountPermissions{
		Enabled:       boolPtr(true),
		Delete:        boolPtr(false),
		SendWhitelist: &PermissionsWhitelistRequest{Enabled: boolPtr(true), UserIDs: &whitelist},
	}
	res, err := f.svc.CreateAccount(ctx, req)
	if err != nil {
		t.Fatalf("CreateAccount: %+v, %v", res, err)
	}
	id := f.createdID(t, res)
	got, found := f.stored(t, id)
	if !found || !got.Enabled || got.Flags.Delete || !got.Flags.Send || !got.Flags.CreateGroup ||
		!got.SendWhitelistEnabled || !slices.Equal(got.SendWhitelist, []int64{colleague.ID}) || got.CommandID != "exec-create-perm-1" {
		t.Fatalf("stored permissions = %+v found=%v; want the given ones on top of the uncontrolled defaults", got, found)
	}
	written, ok := res.Details["permissions"].(AccountPermissionsResult)
	if !ok || written.UserID != id || !written.Enabled || written.Delete || !slices.Equal(written.SendWhitelist.UserIDs, []int64{colleague.ID}) {
		t.Fatalf("details permissions = %#v; want the written permissions", res.Details["permissions"])
	}
	if res.Details["permissions_source"] != "request" {
		t.Fatalf("permissions_source = %v, want request", res.Details["permissions_source"])
	}
	// 命令记录（审计的来源）里同样记着最终写入的权限。
	var recorded struct {
		Details struct {
			Permissions       AccountPermissionsResult `json:"permissions"`
			PermissionsSource string                   `json:"permissions_source"`
		} `json:"details"`
	}
	if err := json.Unmarshal(f.repo.items["exec-create-perm-1"].ResultJSON, &recorded); err != nil {
		t.Fatal(err)
	}
	if p := recorded.Details.Permissions; p.UserID != id || !p.Enabled || p.Delete || !slices.Equal(p.SendWhitelist.UserIDs, []int64{colleague.ID}) ||
		recorded.Details.PermissionsSource != "request" {
		t.Fatalf("recorded result details = %+v; want the written permissions", recorded.Details)
	}
}

// 带登录密码时，账号、密码与权限在同一个事务里写入。
func TestCreateAccountWithPasswordAlsoWritesPermissions(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()
	passwords := memory.NewLoginPasswordStore(f.users)
	permStore := memory.NewAccountPermissionsStore(f.users)
	f.perms = permissions.NewService(permStore, f.users)
	accounts := memory.NewAccountCreationStore(f.users, passwords, permStore)
	f.svc.Configure(Dependencies{
		Permissions: f.perms,
		LoginPasswords: loginpassword.NewService(f.users, passwords, accounts,
			memory.NewLoginPasswordChallengeStore(time.Now), loginPasswordFlows{}),
		LoginPasswordFingerprintKey: DeriveLoginPasswordFingerprintKey("admin-api-token"),
	})

	req := createAccountRequest("exec-create-perm-6", "15550170006")
	req.Password = "first day"
	req.Permissions = &CreateAccountPermissions{Enabled: boolPtr(true), AddContact: boolPtr(false)}
	res, err := f.svc.CreateAccount(ctx, req)
	if err != nil || res.Details["login_password"] != true || res.Details["permissions_source"] != "request" {
		t.Fatalf("CreateAccount = %+v, %v", res, err)
	}
	id := f.createdID(t, res)
	if _, has, _ := passwords.GetLoginPassword(ctx, id); !has {
		t.Fatal("the account has no login password")
	}
	if got, found := f.stored(t, id); !found || !got.Enabled || got.Flags.AddContact {
		t.Fatalf("stored permissions = %+v found=%v; want the given ones", got, found)
	}
}

// 白名单不合法时在进入命令通道前拒绝：不建号，也不留命令记录；试运行与真正执行一样。
func TestCreateAccountRejectsInvalidWhitelistWithoutAccountOrCommandRecord(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()
	gone := f.user(t, domain.User{Deleted: true})
	tooMany := make([]int64, domain.MaxSendWhitelistSize+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	cases := map[string][]int64{
		"missing":  {987654321},
		"deleted":  {gone.ID},
		"too many": tooMany,
	}
	n := 0
	for name, ids := range cases {
		for _, dry := range []bool{false, true} {
			n++
			req := createAccountRequest(fmt.Sprintf("exec-create-perm-bad-%d", n), "15550170010")
			req.DryRun = dry
			whitelist := ids
			req.Permissions = &CreateAccountPermissions{Enabled: boolPtr(true), SendWhitelist: &PermissionsWhitelistRequest{UserIDs: &whitelist}}
			res, err := f.svc.CreateAccount(ctx, req)
			if !errors.Is(err, domain.ErrPermissionWhitelistInvalid) || ErrorCode(err) != "PERMISSION_WHITELIST_INVALID" {
				t.Fatalf("%s (dry=%v) = %+v, %v (code %q); want PERMISSION_WHITELIST_INVALID", name, dry, res, err, ErrorCode(err))
			}
			if _, found, _ := f.users.ByPhone(ctx, "15550170010"); found {
				t.Fatalf("%s (dry=%v): a rejected creation left an account", name, dry)
			}
			if cmd, recorded := f.repo.items[req.CommandID]; recorded {
				t.Fatalf("%s (dry=%v): a rejected creation left a command record: %+v", name, dry, cmd)
			}
		}
	}
}

// 已有记录的 command id 先按记录重放，不再重新校验白名单（与 account.set_permissions 一致）。
func TestCreateAccountReplaysARecordedCommandBeforeCheckingTheWhitelist(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()
	colleague := f.user(t, domain.User{})

	whitelist := []int64{colleague.ID}
	req := createAccountRequest("exec-create-perm-replay", "15550170020")
	req.Permissions = &CreateAccountPermissions{Enabled: boolPtr(true), SendWhitelist: &PermissionsWhitelistRequest{UserIDs: &whitelist}}
	first, err := f.svc.CreateAccount(ctx, req)
	if err != nil {
		t.Fatalf("CreateAccount: %+v, %v", first, err)
	}
	f.deleteUser(t, colleague.ID)

	again, err := f.svc.CreateAccount(ctx, req)
	if err != nil || !again.AlreadyExecuted || again.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("replay = %+v, %v; want the recorded result", again, err)
	}
	other := req
	other.FirstName = "Sam"
	if conflict, err := f.svc.CreateAccount(ctx, other); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict = %+v, %v; want COMMAND_ID_CONFLICT", conflict, err)
	}
}

func TestCreateAccountDryRunValidatesPermissionsAndWritesNothing(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()
	colleague := f.user(t, domain.User{})

	whitelist := []int64{colleague.ID}
	req := createAccountRequest("dry-create-perm-4", "15550170004")
	req.DryRun = true
	req.Permissions = &CreateAccountPermissions{Enabled: boolPtr(true), Edit: boolPtr(false), SendWhitelist: &PermissionsWhitelistRequest{UserIDs: &whitelist}}
	res, err := f.svc.CreateAccount(ctx, req)
	if err != nil || !res.DryRun {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if p, ok := res.Details["permissions"].(AccountPermissionsResult); !ok || !p.Enabled || p.Edit || !slices.Equal(p.SendWhitelist.UserIDs, []int64{colleague.ID}) ||
		res.Details["permissions_source"] != "request" {
		t.Fatalf("dry run details = %+v; want the permissions that would be written", res.Details)
	}

	// 没给 permissions 时试运行展示模板快照。
	f.setDefaults(t)
	tmpl := createAccountRequest("dry-create-perm-5", "15550170004")
	tmpl.DryRun = true
	res, err = f.svc.CreateAccount(ctx, tmpl)
	if p, ok := res.Details["permissions"].(AccountPermissionsResult); err != nil || !ok || !p.Enabled || p.CreateGroup || res.Details["permissions_source"] != "defaults" {
		t.Fatalf("dry run with a template = %+v, %v; want the template snapshot", res, err)
	}

	if _, found, _ := f.users.ByPhone(ctx, "15550170004"); found {
		t.Fatal("dry run created an account")
	}
	if _, found := f.stored(t, colleague.ID); found {
		t.Fatal("dry run wrote permissions")
	}
}

// 没给 permissions 时套用模板快照；模板没开"自助注册也套用"也照样套用。
func TestCreateAccountAppliesTheDefaultsTemplateWhenNoPermissionsAreGiven(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()
	colleague := f.user(t, domain.User{})
	f.setDefaults(t, colleague.ID)

	res, err := f.svc.CreateAccount(ctx, createAccountRequest("exec-create-perm-2", "15550170002"))
	if err != nil {
		t.Fatalf("CreateAccount: %+v, %v", res, err)
	}
	id := f.createdID(t, res)
	got, found := f.stored(t, id)
	if !found || !got.Enabled || got.Flags.CreateGroup || !got.Flags.Send ||
		!got.SendWhitelistEnabled || !slices.Equal(got.SendWhitelist, []int64{colleague.ID}) || got.CommandID != "exec-create-perm-2" {
		t.Fatalf("stored permissions = %+v found=%v; want the template snapshot", got, found)
	}
	if res.Details["permissions_source"] != "defaults" {
		t.Fatalf("permissions_source = %v, want defaults", res.Details["permissions_source"])
	}
	if written, ok := res.Details["permissions"].(AccountPermissionsResult); !ok || written.UserID != id || written.CreateGroup {
		t.Fatalf("details permissions = %#v; want the snapshot", res.Details["permissions"])
	}

	// 快照：之后改模板不影响已建的账号。
	off := defaultsRequest("exec-defaults-off")
	off.Enabled = boolPtr(false)
	if _, err := f.svc.SetPermissionDefaults(ctx, off); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.stored(t, id); !again.Enabled {
		t.Fatal("changing the template changed an existing account")
	}
}

func TestCreateAccountWithoutTemplateWritesNoPermissions(t *testing.T) {
	f := newCreateAccountPermissionsFixture(t)
	ctx := context.Background()

	res, err := f.svc.CreateAccount(ctx, createAccountRequest("exec-create-perm-3", "15550170003"))
	if err != nil {
		t.Fatalf("CreateAccount: %+v, %v", res, err)
	}
	if _, found := f.stored(t, f.createdID(t, res)); found {
		t.Fatal("an account created without a template got a permissions record")
	}
	if _, ok := res.Details["permissions"]; ok {
		t.Fatalf("details = %+v, want no permissions key", res.Details)
	}
	const want = `{"command_id":"exec-create-perm-3","actor":"ops","reason":"new hire","dry_run":false,"phone":"15550170003","first_name":"Pat"}`
	if got := string(f.repo.items["exec-create-perm-3"].RequestJSON); got != want {
		t.Fatalf("recorded request = %s, want %s", got, want)
	}
}
