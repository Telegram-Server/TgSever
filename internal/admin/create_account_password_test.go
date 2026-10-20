package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	authapp "telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台建号时设置登录密码（account.create 的可选 password，#89）的命令层测试。建号用真实的
// auth 服务，登录密码用真实的 loginpassword.Service，两者共用同一套内存存储；建好之后能不能用
// 这个密码登录，由 td 客户端的 SRP 实现算出证明来验证。

// newCreateAccountPasswordFixture 在 newLoginPasswordFixture 的基础上接上真实的 auth 服务做建号。
func newCreateAccountPasswordFixture(t *testing.T) *loginPasswordFixture {
	t.Helper()
	f := newLoginPasswordFixture(t)
	authSvc := authapp.NewService(f.users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	f.svc.Configure(Dependencies{AccountCreator: authSvc})
	return f
}

func createAccountWithPasswordRequest(commandID, phone, password string) CreateAccountRequest {
	return CreateAccountRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "new hire"},
		Phone:       phone,
		FirstName:   "Pat",
		Password:    password,
	}
}

func TestCreateAccountWithPasswordCanSignInRightAway(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()

	res, err := f.svc.CreateAccount(ctx, createAccountWithPasswordRequest("exec-create-pw-1", "+1 555 009 0001", "first day"))
	if err != nil {
		t.Fatalf("CreateAccount with a password: %+v, %v", res, err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Details["login_password"] != true {
		t.Fatalf("result = %+v, want completed with login_password=true", res)
	}
	created, found, err := f.users.ByPhone(ctx, "15550090001")
	if err != nil || !found || res.Details["user_id"] != created.ID {
		t.Fatalf("created user=%+v found=%v err=%v details=%+v", created, found, err, res.Details)
	}
	if err := f.signIn(t, "+15550090001", f.challenge(t, "+15550090001"), "first day"); err != nil {
		t.Fatalf("sign in with the password set at creation: %v", err)
	}
}

func TestCreateAccountWithoutPasswordRecordsTheSameRequestAsBefore(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()

	res, err := f.svc.CreateAccount(ctx, createAccountWithPasswordRequest("exec-create-pw-2", "15550090002", ""))
	if err != nil {
		t.Fatalf("CreateAccount without a password: %+v, %v", res, err)
	}
	if _, ok := res.Details["login_password"]; ok {
		t.Fatalf("details = %+v, want no login_password key", res.Details)
	}
	created, _, _ := f.users.ByPhone(ctx, "15550090002")
	if _, has, _ := f.passwords.GetLoginPassword(ctx, created.ID); has {
		t.Fatal("an account created without a password got a login password")
	}
	const want = `{"command_id":"exec-create-pw-2","actor":"ops","reason":"new hire","dry_run":false,"phone":"15550090002","first_name":"Pat"}`
	if got := string(f.repo.items["exec-create-pw-2"].RequestJSON); got != want {
		t.Fatalf("recorded request = %s, want %s", got, want)
	}
}

func TestCreateAccountWithPasswordDryRunWritesNothing(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()

	req := createAccountWithPasswordRequest("dry-create-pw-3", "15550090003", "dry password")
	req.DryRun = true
	res, err := f.svc.CreateAccount(ctx, req)
	if err != nil || !res.DryRun || res.Details["login_password"] != true {
		t.Fatalf("dry run = %+v, %v; want a completed dry run with login_password=true", res, err)
	}
	if _, found, _ := f.users.ByPhone(ctx, "15550090003"); found {
		t.Fatal("dry run created an account")
	}

	bad := createAccountWithPasswordRequest("dry-create-pw-3-bad", "15550090003", strings.Repeat("p", loginpassword.MaxPlaintextPasswordBytes+1))
	bad.DryRun = true
	if res, err := f.svc.CreateAccount(ctx, bad); err == nil || res.Code != "LOGIN_PASSWORD_INVALID" {
		t.Fatalf("dry run with a too long password = %+v, %v; want LOGIN_PASSWORD_INVALID", res, err)
	}
}

// 建号失败（号码冲突、密码不合规）时什么都不留下。
func TestCreateAccountWithPasswordLeavesNothingWhenRejected(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()
	existing := f.user(t, domain.User{Phone: "15550090004", FirstName: "Taken"})

	res, err := f.svc.CreateAccount(ctx, createAccountWithPasswordRequest("exec-create-pw-4a", "15550090004", "secret"))
	if !errors.Is(err, domain.ErrPhoneNumberOccupied) || res.Code != "PHONE_NUMBER_OCCUPIED" {
		t.Fatalf("occupied number = %+v, %v; want PHONE_NUMBER_OCCUPIED", res, err)
	}
	if _, has, _ := f.passwords.GetLoginPassword(ctx, existing.ID); has {
		t.Fatal("a rejected creation gave the existing account a login password")
	}

	res, err = f.svc.CreateAccount(ctx, createAccountWithPasswordRequest("exec-create-pw-4b", "15550090005", "bad\xffutf8"))
	if err == nil || res.Code != "LOGIN_PASSWORD_INVALID" {
		t.Fatalf("invalid password = %+v, %v; want LOGIN_PASSWORD_INVALID", res, err)
	}
	if _, found, _ := f.users.ByPhone(ctx, "15550090005"); found {
		t.Fatal("an invalid password still created the account")
	}
	if f.repo.items["exec-create-pw-4b"].Status != domain.AdminCommandFailed {
		t.Fatalf("rejection not recorded as failed: %+v", f.repo.items["exec-create-pw-4b"])
	}
}

// failingLoginPasswordStore 写登录密码时失败：建号和写密码在同一个事务里，账号也不能留下。
type failingLoginPasswordStore struct {
	*memory.LoginPasswordStore
}

var errLoginPasswordWrite = errors.New("login password write failed")

func (failingLoginPasswordStore) CreateUserWithLoginPassword(context.Context, domain.User, domain.LoginPassword) (domain.User, error) {
	return domain.User{}, errLoginPasswordWrite
}

func TestCreateAccountWithPasswordCreatesNoAccountWhenThePasswordWriteFails(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()
	failing := loginpassword.NewService(f.users, failingLoginPasswordStore{f.passwords},
		memory.NewLoginPasswordChallengeStore(time.Now), loginPasswordFlows{})
	f.svc.Configure(Dependencies{LoginPasswords: failing})

	res, err := f.svc.CreateAccount(ctx, createAccountWithPasswordRequest("exec-create-pw-5", "15550090006", "secret"))
	if !errors.Is(err, errLoginPasswordWrite) || res.Status != string(domain.AdminCommandFailed) {
		t.Fatalf("failed password write = %+v, %v; want the write error", res, err)
	}
	if _, found, _ := f.users.ByPhone(ctx, "15550090006"); found {
		t.Fatal("the account was created although its login password was not")
	}
}

func TestCreateAccountWithPasswordIsIdempotentAndConflictsOnAnotherPassword(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()
	req := createAccountWithPasswordRequest("exec-create-pw-6", "15550090007", "first")

	first, err := f.svc.CreateAccount(ctx, req)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	again, err := f.svc.CreateAccount(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("replay = %+v, %v; want an already-executed replay of %+v", again, err, first)
	}

	other := req
	other.Password = "second"
	if res, err := f.svc.CreateAccount(ctx, other); err == nil || res.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("same command id with another password = %+v, %v; want COMMAND_ID_CONFLICT", res, err)
	}
	withoutPassword := req
	withoutPassword.Password = ""
	if res, err := f.svc.CreateAccount(ctx, withoutPassword); err == nil || res.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("same command id without the password = %+v, %v; want COMMAND_ID_CONFLICT", res, err)
	}
	// 调用方塞进来的指纹不算数：服务端总是按密码重算。
	forged := other
	forged.PasswordFingerprint = storedFingerprint(t, f.repo.items["exec-create-pw-6"])
	if res, err := f.svc.CreateAccount(ctx, forged); err == nil || res.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("forged fingerprint = %+v, %v; want COMMAND_ID_CONFLICT", res, err)
	}
	if err := f.signIn(t, "+15550090007", f.challenge(t, "+15550090007"), "first"); err != nil {
		t.Fatalf("a conflicting request changed the password: %v", err)
	}
}

func TestCreateAccountWithPasswordNeverRecordsThePassword(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()
	const password = "Create-plaintext-marker-5b9e"

	dry := createAccountWithPasswordRequest("dry-create-pw-7", "15550090008", password)
	dry.DryRun = true
	var results []CommandResult
	for _, req := range []CreateAccountRequest{dry, createAccountWithPasswordRequest("exec-create-pw-7", "15550090008", password)} {
		res, err := f.svc.CreateAccount(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.CommandID, err)
		}
		results = append(results, res)
	}
	// 不带密钥的摘要（任何人拿候选密码都能离线算出来）同样不能出现。
	sum := sha256.Sum256([]byte(password))
	forbidden := []string{password, hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:])}
	for _, id := range []string{"dry-create-pw-7", "exec-create-pw-7"} {
		cmd := f.repo.items[id]
		storedFingerprint(t, cmd)
		for _, text := range []string{string(cmd.RequestJSON), string(cmd.ResultJSON), cmd.Error, cmd.Reason} {
			for _, bad := range forbidden {
				if strings.Contains(text, bad) {
					t.Fatalf("%s recorded the password (or an unkeyed digest of it): %s", id, text)
				}
			}
		}
	}
	for _, res := range results {
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), password) || strings.Contains(string(raw), "fingerprint") {
			t.Fatalf("command result leaks the password or its fingerprint: %s", raw)
		}
	}
}

func TestCreateAccountWithPasswordRequiresAFingerprintKey(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), AccountCreator: f.svc.accountCreator,
		LoginPasswords: f.svc.loginPasswords, Now: fixedNow})
	if _, err := svc.CreateAccount(context.Background(), createAccountWithPasswordRequest("exec-create-pw-8", "15550090009", "secret")); err == nil {
		t.Fatal("CreateAccount with a password but no fingerprint key succeeded")
	}
	if _, found, _ := f.users.ByPhone(context.Background(), "15550090009"); found {
		t.Fatal("an account was created without a fingerprint key")
	}
}
