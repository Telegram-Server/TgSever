package admin

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	authapp "telesrv/internal/app/auth"
	"telesrv/internal/app/channels"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/onboarding"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台设置入驻模板（onboarding_defaults.set）的命令层测试：试运行、按 command id 幂等与冲突、
// 写审计、不合法的模板在进入命令通道之前被拒绝。模板校验的细则在 app/onboarding 测。

type onboardingDefaultsFixture struct {
	*permissionsFixture
	channels   *memory.ChannelStore
	onboarding *onboarding.Service
}

func newOnboardingDefaultsFixture(t *testing.T) *onboardingDefaultsFixture {
	t.Helper()
	f := &onboardingDefaultsFixture{
		permissionsFixture: &permissionsFixture{repo: newMemoryCommandRepo(), users: memory.NewUserStore()},
		channels:           memory.NewChannelStore(),
	}
	f.onboarding = onboarding.NewService(memory.NewOnboardingStore(), f.users, contacts.NewService(memory.NewContactStore(), f.users), channels.NewService(f.channels))
	f.svc = NewService(Dependencies{
		Commands:           f.repo,
		Users:              users.NewService(f.users),
		OnboardingDefaults: f.onboarding,
		Now:                fixedNow,
	})
	return f
}

func (f *onboardingDefaultsFixture) group(t *testing.T, owner int64) int64 {
	t.Helper()
	res, err := f.channels.CreateChannel(context.Background(), domain.CreateChannelRequest{CreatorUserID: owner, Title: "Lobby", Megagroup: true, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	return res.Channel.ID
}

func onboardingRequest(commandID string) SetOnboardingDefaultsRequest {
	return SetOnboardingDefaultsRequest{CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "new staff onboarding"}}
}

func TestOnboardingDefaultsWithoutTemplateIsEmpty(t *testing.T) {
	f := newOnboardingDefaultsFixture(t)
	result, err := f.svc.OnboardingDefaults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Found || result.Contacts == nil || len(result.Contacts) != 0 || result.ChannelIDs == nil || len(result.ChannelIDs) != 0 {
		t.Fatalf("defaults without a template = %+v, want not found with empty lists", result)
	}
}

func TestSetOnboardingDefaultsReplacesTheTemplateAndRecordsTheCommand(t *testing.T) {
	f := newOnboardingDefaultsFixture(t)
	ctx := context.Background()
	support, owner := f.user(t, domain.User{}), f.user(t, domain.User{})
	group := f.group(t, owner.ID)

	req := onboardingRequest("exec-onboarding-1")
	req.Contacts = []OnboardingContactSetting{{UserID: support.ID, Mutual: true}, {UserID: owner.ID}}
	req.ChannelIDs = []int64{group}
	res, err := f.svc.SetOnboardingDefaults(ctx, req)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionSetOnboardingDefaults || res.Details["would_change"] != true {
		t.Fatalf("set result = %+v", res)
	}
	got, err := f.svc.OnboardingDefaults(ctx)
	if err != nil || !got.Found || !reflect.DeepEqual(got.Contacts, req.Contacts) || !reflect.DeepEqual(got.ChannelIDs, req.ChannelIDs) || got.CommandID != "exec-onboarding-1" {
		t.Fatalf("OnboardingDefaults = %+v, %v", got, err)
	}
	if cmd := f.repo.items["exec-onboarding-1"]; cmd.Action != ActionSetOnboardingDefaults || cmd.Actor != "ops" || cmd.TargetUserID != 0 {
		t.Fatalf("command not recorded: %+v", cmd)
	}

	// 整体替换：不给的名单变成空。
	clear := onboardingRequest("exec-onboarding-2")
	clear.Contacts = []OnboardingContactSetting{{UserID: owner.ID}}
	if _, err := f.svc.SetOnboardingDefaults(ctx, clear); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = f.svc.OnboardingDefaults(ctx)
	if !reflect.DeepEqual(got.Contacts, clear.Contacts) || len(got.ChannelIDs) != 0 {
		t.Fatalf("after replace = %+v, want only the owner and no channels", got)
	}
}

func TestSetOnboardingDefaultsDryRunWritesNothing(t *testing.T) {
	f := newOnboardingDefaultsFixture(t)
	support := f.user(t, domain.User{})
	req := onboardingRequest("dry-onboarding")
	req.Contacts = []OnboardingContactSetting{{UserID: support.ID}}
	req.DryRun = true
	res, err := f.svc.SetOnboardingDefaults(context.Background(), req)
	if err != nil || !res.DryRun || res.Details["would_change"] != true {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if got, _ := f.svc.OnboardingDefaults(context.Background()); got.Found {
		t.Fatal("dry run wrote the template")
	}
}

func TestSetOnboardingDefaultsIsIdempotentPerCommandIDAndConflictsOnAnotherValue(t *testing.T) {
	f := newOnboardingDefaultsFixture(t)
	ctx := context.Background()
	a, b := f.user(t, domain.User{}), f.user(t, domain.User{})
	req := onboardingRequest("exec-onboarding-replay")
	req.Contacts = []OnboardingContactSetting{{UserID: a.ID}}
	if _, err := f.svc.SetOnboardingDefaults(ctx, req); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.SetOnboardingDefaults(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	other := req
	other.Contacts = []OnboardingContactSetting{{UserID: b.ID}}
	if conflict, err := f.svc.SetOnboardingDefaults(ctx, other); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict = %+v, %v", conflict, err)
	}
}

// 后台建号既不带登录密码、也没有权限模板时，也要经原子建号操作建号，入驻任务才会写入。
func TestCreateAccountWithoutPasswordOrPermissionsStillEnqueuesOnboarding(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	onboardingStore := memory.NewOnboardingStore()
	support, err := userStore.Create(ctx, domain.User{AccessHash: 1, FirstName: "Support", Phone: "15550450001"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := onboardingStore.SetOnboardingDefaults(ctx, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: support.ID}}}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Dependencies{
		Commands:        newMemoryCommandRepo(),
		Users:           users.NewService(userStore),
		AccountCreator:  authapp.NewService(userStore, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345"),
		AccountCreation: memory.NewAccountCreationStore(userStore, nil, nil).WithOnboarding(onboardingStore),
		Now:             fixedNow,
	})

	res, err := svc.CreateAccount(ctx, createAccountRequest("exec-create-onboarding", "15550450002"))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	jobs, err := onboardingStore.ClaimOnboardingJobs(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].UserID != res.Details["user_id"] {
		t.Fatalf("onboarding jobs = %+v, want one for the created account %v", jobs, res.Details["user_id"])
	}
}

func TestSetOnboardingDefaultsRejectsAnInvalidTemplateBeforeTheCommandChannel(t *testing.T) {
	f := newOnboardingDefaultsFixture(t)
	for _, dry := range []bool{true, false} {
		req := onboardingRequest("bad-onboarding")
		req.DryRun = dry
		req.Contacts = []OnboardingContactSetting{{UserID: 987654321}}
		_, err := f.svc.SetOnboardingDefaults(context.Background(), req)
		if ErrorCode(err) != "ONBOARDING_DEFAULTS_INVALID" || !errors.Is(err, domain.ErrOnboardingDefaultsInvalid) {
			t.Fatalf("dry=%v err = %v (code %q), want ONBOARDING_DEFAULTS_INVALID", dry, err, ErrorCode(err))
		}
		if _, recorded := f.repo.items[req.CommandID]; recorded {
			t.Fatal("invalid template was recorded as a command")
		}
	}
	if got, _ := f.svc.OnboardingDefaults(context.Background()); got.Found {
		t.Fatal("invalid template was written")
	}
}
