package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	authapp "telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// fakeAccountCreator stands in for the auth service: it normalizes the phone
// by stripping non-digits, rejects occupied numbers and records creations.
type fakeAccountCreator struct {
	byPhone     map[string]domain.User
	nextID      int64
	createCalls int
	// lookups counts phone-ownership checks (the auth service's ByPhone).
	lookups int
}

func newFakeAccountCreator() *fakeAccountCreator {
	return &fakeAccountCreator{byPhone: map[string]domain.User{}, nextID: 5000}
}

func (f *fakeAccountCreator) ValidateNewAccount(_ context.Context, phone, firstName, lastName string) (domain.User, error) {
	phone = domain.NormalizePhone(phone)
	if !domain.ValidPhone(phone) {
		return domain.User{}, domain.ErrPhoneNumberInvalid
	}
	if strings.TrimSpace(firstName) == "" {
		return domain.User{}, domain.ErrFirstNameInvalid
	}
	f.lookups++
	if _, taken := f.byPhone[phone]; taken {
		return domain.User{}, domain.ErrPhoneNumberOccupied
	}
	return domain.User{Phone: phone, FirstName: strings.TrimSpace(firstName), LastName: strings.TrimSpace(lastName), PremiumUntil: 1234}, nil
}

func (f *fakeAccountCreator) CreateAccount(ctx context.Context, phone, firstName, lastName string) (domain.User, error) {
	return f.CreateAccountWith(ctx, phone, firstName, lastName, nil)
}

func (f *fakeAccountCreator) CreateAccountWith(ctx context.Context, phone, firstName, lastName string, create authapp.CreateAccountFunc) (domain.User, error) {
	u, err := f.ValidateNewAccount(ctx, phone, firstName, lastName)
	if err != nil {
		return domain.User{}, err
	}
	f.createCalls++
	f.nextID++
	u.ID = f.nextID
	if create != nil {
		if u, err = create(ctx, u); err != nil {
			return domain.User{}, err
		}
	}
	f.byPhone[u.Phone] = u
	return u, nil
}

func TestCreateAccountDryRunExecuteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCommandRepo()
	creator := newFakeAccountCreator()
	svc := NewService(Dependencies{Commands: repo, AccountCreator: creator, Now: fixedNow})

	dry, err := svc.CreateAccount(ctx, CreateAccountRequest{
		CommandMeta: CommandMeta{CommandID: "dry-create-account", Actor: "ops", Reason: "new hire", DryRun: true},
		Phone:       "+1 555 000 7811",
		FirstName:   "Dana",
	})
	if err != nil {
		t.Fatalf("dry-run CreateAccount: %v", err)
	}
	if !dry.DryRun || dry.Status != string(domain.AdminCommandCompleted) || creator.createCalls != 0 {
		t.Fatalf("dry-run result=%+v createCalls=%d, want completed dry run without creating", dry, creator.createCalls)
	}
	if dry.Details["phone"] != "15550007811" || dry.Details["first_name"] != "Dana" || dry.Details["premium_until"] != 1234 {
		t.Fatalf("dry-run details = %+v, want normalized account preview", dry.Details)
	}
	if repo.items["dry-create-account"].Action != ActionCreateAccount {
		t.Fatalf("dry run not recorded as %s: %+v", ActionCreateAccount, repo.items["dry-create-account"])
	}

	execReq := CreateAccountRequest{
		CommandMeta: CommandMeta{CommandID: "exec-create-account", Actor: "ops", Reason: "new hire"},
		Phone:       "+1 555 000 7811",
		FirstName:   "Dana",
		LastName:    "Ops",
	}
	creator.lookups = 0
	exec, err := svc.CreateAccount(ctx, execReq)
	if err != nil {
		t.Fatalf("execute CreateAccount: %v", err)
	}
	if creator.lookups != 1 {
		t.Fatalf("execute checked the phone %d times, want once (the creator validates itself)", creator.lookups)
	}
	if exec.Status != string(domain.AdminCommandCompleted) || creator.createCalls != 1 || exec.Details["user_id"] != int64(5001) {
		t.Fatalf("execute result=%+v createCalls=%d", exec, creator.createCalls)
	}
	stored := repo.items["exec-create-account"]
	if stored.Status != domain.AdminCommandCompleted || stored.Actor != "ops" || stored.Reason != "new hire" {
		t.Fatalf("stored command = %+v, want completed with actor and reason", stored)
	}

	again, err := svc.CreateAccount(ctx, execReq)
	if err != nil {
		t.Fatalf("replay CreateAccount: %v", err)
	}
	if !again.AlreadyExecuted || creator.createCalls != 1 {
		t.Fatalf("replay result=%+v createCalls=%d, want idempotent replay creating no second account", again, creator.createCalls)
	}

	other := execReq
	other.Phone = "+1 555 000 7812"
	conflict, err := svc.CreateAccount(ctx, other)
	if err == nil || conflict.Error != "COMMAND_ID_CONFLICT" || creator.createCalls != 1 {
		t.Fatalf("reused command id result=%+v err=%v createCalls=%d, want COMMAND_ID_CONFLICT", conflict, err, creator.createCalls)
	}
}

func TestCreateAccountOccupiedPhoneFailsWithClearCodeAndIsRecorded(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCommandRepo()
	creator := newFakeAccountCreator()
	creator.byPhone["15550007813"] = domain.User{ID: 42, Phone: "15550007813"}
	svc := NewService(Dependencies{Commands: repo, AccountCreator: creator, Now: fixedNow})

	for _, dryRun := range []bool{true, false} {
		id := "occupied-exec"
		if dryRun {
			id = "occupied-dry"
		}
		res, err := svc.CreateAccount(ctx, CreateAccountRequest{
			CommandMeta: CommandMeta{CommandID: id, Actor: "ops", Reason: "dup", DryRun: dryRun},
			Phone:       "15550007813",
			FirstName:   "Eve",
		})
		if !errors.Is(err, domain.ErrPhoneNumberOccupied) {
			t.Fatalf("dryRun=%v err=%v, want ErrPhoneNumberOccupied", dryRun, err)
		}
		if res.Status != string(domain.AdminCommandFailed) || res.Code != "PHONE_NUMBER_OCCUPIED" {
			t.Fatalf("dryRun=%v result=%+v, want failed with PHONE_NUMBER_OCCUPIED", dryRun, res)
		}
		if repo.items[id].Status != domain.AdminCommandFailed {
			t.Fatalf("dryRun=%v rejected command not recorded as failed: %+v", dryRun, repo.items[id])
		}
	}
	if creator.createCalls != 0 {
		t.Fatalf("createCalls=%d, want none", creator.createCalls)
	}
}

func TestCreateAccountInvalidInputIsRecordedAsFailed(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCommandRepo()
	creator := newFakeAccountCreator()
	svc := NewService(Dependencies{Commands: repo, AccountCreator: creator, Now: fixedNow})

	res, err := svc.CreateAccount(ctx, CreateAccountRequest{
		CommandMeta: CommandMeta{CommandID: "bad-phone", Actor: "ops", Reason: "typo"},
		Phone:       "not-a-phone",
		FirstName:   "Eve",
	})
	if !errors.Is(err, domain.ErrPhoneNumberInvalid) || res.Error == "" {
		t.Fatalf("invalid phone result=%+v err=%v, want the creator's validation error", res, err)
	}
	if repo.items["bad-phone"].Status != domain.AdminCommandFailed || creator.createCalls != 0 {
		t.Fatalf("invalid phone stored=%+v createCalls=%d", repo.items["bad-phone"], creator.createCalls)
	}
}
