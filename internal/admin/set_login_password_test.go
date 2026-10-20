package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tdsrp "github.com/iamxvbaba/td/crypto/srp"

	authapp "telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store/memory"
)

// 后台设置/重设登录密码命令（account.set_login_password）的命令层测试。登录密码服务用真实的
// loginpassword.Service 和内存存储：设置之后能不能用这个密码登录，由 td 客户端的 SRP 实现算出
// 证明来验证，与客户端真正登录走同一套数学。

var loginPasswordTestAuthKey = [8]byte{9, 8, 7, 6, 5, 4, 3, 2}

// loginPasswordFlows 是登录收尾的替身：证明成立之后直接算登录成功。
type loginPasswordFlows struct{}

func (loginPasswordFlows) CheckSignUpAllowed(context.Context) error { return nil }

func (loginPasswordFlows) SignUpWithAccountCreator(ctx context.Context, _ domain.Authorization, phone, firstName, lastName string, create authapp.CreateAccountFunc) (domain.User, error) {
	return create(ctx, domain.User{Phone: phone, FirstName: firstName, LastName: lastName})
}

func (loginPasswordFlows) FinishVerifiedSignIn(_ context.Context, _ domain.Authorization, u domain.User) (domain.User, error) {
	return u, nil
}

// recordedNotices 记录 777000 发出的通知。
type recordedNotices struct {
	mu   sync.Mutex
	sent []domain.SendPrivateTextRequest
}

func (r *recordedNotices) SendPrivateText(_ context.Context, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, req)
	return domain.SendPrivateTextResult{}, nil
}

func (r *recordedNotices) messages() []domain.SendPrivateTextRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.SendPrivateTextRequest(nil), r.sent...)
}

type loginPasswordFixture struct {
	svc       *Service
	repo      *memoryCommandRepo
	users     *memory.UserStore
	passwords *memory.LoginPasswordStore
	login     *loginpassword.Service
	notices   *recordedNotices
	auth      *fakeAuthService
	revoker   *fakeRevoker
}

// newLoginPasswordFixture 起一个密码登录总开关关闭的服务（后台设置密码不受总开关限制），
// login 是同一套存储上总开关打开的服务，只用来验证设置之后能否登录。
func newLoginPasswordFixture(t *testing.T) *loginPasswordFixture {
	t.Helper()
	f := &loginPasswordFixture{
		repo:    newMemoryCommandRepo(),
		users:   memory.NewUserStore(),
		notices: &recordedNotices{},
		auth:    &fakeAuthService{},
		revoker: &fakeRevoker{},
	}
	f.passwords = memory.NewLoginPasswordStore(f.users)
	challenges := memory.NewLoginPasswordChallengeStore(time.Now)
	admin := loginpassword.NewService(f.users, f.passwords, challenges, loginPasswordFlows{},
		loginpassword.WithChangeNotices(f.notices, nil))
	f.login = loginpassword.NewService(f.users, f.passwords, challenges, loginPasswordFlows{},
		loginpassword.WithEnabled(true))
	f.svc = NewService(Dependencies{
		Commands:                    f.repo,
		Auth:                        f.auth,
		Revoker:                     f.revoker,
		LoginPasswords:              admin,
		LoginPasswordFingerprintKey: DeriveLoginPasswordFingerprintKey("admin-api-token"),
		Now:                         fixedNow,
	})
	return f
}

func (f *loginPasswordFixture) user(t *testing.T, u domain.User) domain.User {
	t.Helper()
	created, err := f.users.Create(context.Background(), u)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

// challenge 像客户端一样调用 getLoginPassword 拿到登录 challenge。
func (f *loginPasswordFixture) challenge(t *testing.T, phone string) domain.PasswordSettings {
	t.Helper()
	settings, err := f.login.GetLoginPassword(context.Background(), loginPasswordTestAuthKey, phone)
	if err != nil {
		t.Fatalf("GetLoginPassword(%s): %v", phone, err)
	}
	return settings
}

// signIn 用 td 客户端的 SRP 实现对 challenge 算出证明并提交 signInWithPassword。
func (f *loginPasswordFixture) signIn(t *testing.T, phone string, settings domain.PasswordSettings, password string) error {
	t.Helper()
	a := make([]byte, srp.HashSize)
	if _, err := rand.Read(a); err != nil {
		t.Fatal(err)
	}
	algo := settings.CurrentAlgo
	answer, err := tdsrp.NewSRP(nil).Hash([]byte(password), settings.SRPB, a, tdsrp.Input{Salt1: algo.Salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P})
	if err != nil {
		t.Fatalf("td Hash: %v", err)
	}
	_, err = f.login.SignIn(context.Background(), domain.Authorization{}, loginPasswordTestAuthKey, phone,
		domain.PasswordCheck{SRPID: settings.SRPID, A: answer.A, M1: answer.M1})
	return err
}

func setLoginPasswordRequest(commandID string, userID int64, password string) SetLoginPasswordRequest {
	return SetLoginPasswordRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "forgot password"},
		UserID:      userID,
		Password:    password,
	}
}

func TestSetLoginPasswordOnAnAccountWithoutOneLetsItSignIn(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088001", FirstName: "Code"})

	res, err := f.svc.SetLoginPassword(ctx, setLoginPasswordRequest("exec-set-pw-1", u.ID, "correct horse"))
	if err != nil {
		t.Fatalf("SetLoginPassword: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.TargetUserID != u.ID || res.Details["had_password"] != false {
		t.Fatalf("result = %+v, want completed for user %d with had_password=false", res, u.ID)
	}
	if err := f.signIn(t, "+15550088001", f.challenge(t, "+15550088001"), "correct horse"); err != nil {
		t.Fatalf("sign in with the password set by the operator: %v", err)
	}
	got := f.notices.messages()
	if len(got) != 1 || got[0].SenderUserID != domain.OfficialSystemUserID || got[0].RecipientUserID != u.ID ||
		!strings.HasPrefix(got[0].Message, "Login password set by an administrator.\n") {
		t.Fatalf("notices = %+v, want one 777000 \"set by an administrator\" notice", got)
	}
}

func TestSetLoginPasswordResetsAnExistingPasswordAndVoidsOldChallenges(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088002", FirstName: "Forgot"})
	if _, err := f.svc.SetLoginPassword(ctx, setLoginPasswordRequest("exec-set-pw-2a", u.ID, "old password")); err != nil {
		t.Fatalf("first SetLoginPassword: %v", err)
	}
	// 重设前签发的 challenge：之后交上来一律 SRP_PASSWORD_CHANGED，哪怕用的是旧密码的正确证明。
	stale := f.challenge(t, "+15550088002")

	res, err := f.svc.SetLoginPassword(ctx, setLoginPasswordRequest("exec-set-pw-2b", u.ID, "new password"))
	if err != nil {
		t.Fatalf("reset SetLoginPassword: %v", err)
	}
	if res.Details["had_password"] != true || res.Message != "login password reset" {
		t.Fatalf("reset result = %+v, want had_password=true and \"login password reset\"", res)
	}
	if err := f.signIn(t, "+15550088002", stale, "old password"); !errors.Is(err, domain.ErrSRPPasswordChanged) {
		t.Fatalf("sign in with a challenge issued before the reset = %v, want ErrSRPPasswordChanged", err)
	}
	if err := f.signIn(t, "+15550088002", f.challenge(t, "+15550088002"), "old password"); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("sign in with the old password = %v, want ErrPasswordHashInvalid", err)
	}
	if err := f.signIn(t, "+15550088002", f.challenge(t, "+15550088002"), "new password"); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	got := f.notices.messages()
	if len(got) != 2 || !strings.HasPrefix(got[1].Message, "Login password reset by an administrator.\n") {
		t.Fatalf("notices = %+v, want the second one to say \"reset by an administrator\"", got)
	}
}

func TestSetLoginPasswordDryRunWritesNothing(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088003", FirstName: "Dry"})
	f.auth.items = []domain.Authorization{{AuthKeyID: [8]byte{1}, UserID: u.ID, Hash: 11}}

	req := setLoginPasswordRequest("dry-set-pw-3", u.ID, "dry password")
	req.DryRun = true
	req.RevokeSessions = true
	res, err := f.svc.SetLoginPassword(ctx, req)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun || res.Status != string(domain.AdminCommandCompleted) || res.Details["had_password"] != false {
		t.Fatalf("dry run result = %+v, want a completed dry run reporting had_password=false", res)
	}
	if _, has, _ := f.passwords.GetLoginPassword(ctx, u.ID); has {
		t.Fatal("dry run stored a login password")
	}
	if len(f.notices.messages()) != 0 || len(f.revoker.keys) != 0 {
		t.Fatalf("dry run sent notices=%d revoked=%v, want neither", len(f.notices.messages()), f.revoker.keys)
	}
	if f.repo.items["dry-set-pw-3"].Action != ActionSetLoginPassword {
		t.Fatalf("dry run not recorded as %s: %+v", ActionSetLoginPassword, f.repo.items["dry-set-pw-3"])
	}
}

// storedFingerprint 读出命令记录里的密码指纹。
func storedFingerprint(t *testing.T, cmd domain.AdminCommand) string {
	t.Helper()
	var req struct {
		Fingerprint string `json:"password_fingerprint"`
	}
	if err := json.Unmarshal(cmd.RequestJSON, &req); err != nil || req.Fingerprint == "" {
		t.Fatalf("request %s has no password fingerprint (err=%v)", cmd.RequestJSON, err)
	}
	return req.Fingerprint
}

func TestSetLoginPasswordIsIdempotentPerCommandIDAndConflictsOnAnotherPassword(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088004", FirstName: "Replay"})
	req := setLoginPasswordRequest("exec-set-pw-4", u.ID, "first")

	if _, err := f.svc.SetLoginPassword(ctx, req); err != nil {
		t.Fatalf("SetLoginPassword: %v", err)
	}
	stored, _, _ := f.passwords.GetLoginPassword(ctx, u.ID)

	again, err := f.svc.SetLoginPassword(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("replay = %+v, %v; want an already-executed replay", again, err)
	}
	if now, _, _ := f.passwords.GetLoginPassword(ctx, u.ID); string(now.Verifier) != string(stored.Verifier) {
		t.Fatal("replay wrote the password again")
	}
	if len(f.notices.messages()) != 1 {
		t.Fatalf("notices after replay = %d, want 1", len(f.notices.messages()))
	}

	other := req
	other.Password = "second"
	conflict, err := f.svc.SetLoginPassword(ctx, other)
	if err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("same command id with another password = %+v, %v; want COMMAND_ID_CONFLICT", conflict, err)
	}

	// 调用方塞进来的指纹不算数：服务端总是按密码重算。
	forged := other
	forged.PasswordFingerprint = storedFingerprint(t, f.repo.items["exec-set-pw-4"])
	if res, err := f.svc.SetLoginPassword(ctx, forged); err == nil || res.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("forged fingerprint = %+v, %v; want COMMAND_ID_CONFLICT", res, err)
	}
	if err := f.signIn(t, "+15550088004", f.challenge(t, "+15550088004"), "first"); err != nil {
		t.Fatalf("a conflicting request changed the password: %v", err)
	}
}

func TestSetLoginPasswordRejectsSystemBotDeletedAndMissingAccounts(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	bot := f.user(t, domain.User{FirstName: "Bot", Bot: true, Username: "pw_bot"})
	deleted := f.user(t, domain.User{Phone: "15550088005", FirstName: "Gone", Deleted: true})

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
				req := setLoginPasswordRequest(fmt.Sprintf("reject-%s-%v", tc.name, dry), tc.userID, "secret")
				req.DryRun = dry
				res, err := f.svc.SetLoginPassword(ctx, req)
				if err == nil || res.Code != "USER_INVALID" {
					t.Fatalf("dry=%v result = %+v, %v; want USER_INVALID", dry, res, err)
				}
				if f.repo.items[req.CommandID].Status != domain.AdminCommandFailed {
					t.Fatalf("dry=%v rejection not recorded as failed: %+v", dry, f.repo.items[req.CommandID])
				}
			}
			if _, has, _ := f.passwords.GetLoginPassword(ctx, tc.userID); has {
				t.Fatal("a rejected account got a login password")
			}
		})
	}
	if len(f.notices.messages()) != 0 {
		t.Fatalf("rejections sent %d notices", len(f.notices.messages()))
	}
}

func TestSetLoginPasswordLengthLimits(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088006", FirstName: "Len"})

	longest := strings.Repeat("p", loginpassword.MaxPlaintextPasswordBytes)
	for i, tc := range []struct {
		password string
		ok       bool
	}{
		{"", false},
		{"x", true},
		{longest, true},
		{longest + "p", false},
		{"bad\xffutf8", false},
	} {
		for _, dry := range []bool{true, false} {
			req := setLoginPasswordRequest(fmt.Sprintf("len-%d-%v", i, dry), u.ID, tc.password)
			req.DryRun = dry
			res, err := f.svc.SetLoginPassword(ctx, req)
			if tc.ok && err != nil {
				t.Fatalf("dry=%v password of %d bytes rejected: %+v, %v", dry, len(tc.password), res, err)
			}
			if !tc.ok && (err == nil || res.Code != "LOGIN_PASSWORD_INVALID") {
				t.Fatalf("dry=%v password of %d bytes = %+v, %v; want LOGIN_PASSWORD_INVALID", dry, len(tc.password), res, err)
			}
		}
	}
	if err := f.signIn(t, "+15550088006", f.challenge(t, "+15550088006"), longest); err != nil {
		t.Fatalf("sign in with the longest password: %v", err)
	}
}

func TestSetLoginPasswordCanSignOutEveryDevice(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088007", FirstName: "Stolen"})
	f.auth.items = []domain.Authorization{
		{AuthKeyID: [8]byte{1}, UserID: u.ID, Hash: 101},
		{AuthKeyID: [8]byte{2}, UserID: u.ID, Hash: 102},
	}

	// 默认不登出。
	if res, err := f.svc.SetLoginPassword(ctx, setLoginPasswordRequest("exec-set-pw-7a", u.ID, "keep")); err != nil || len(f.revoker.keys) != 0 {
		t.Fatalf("without revoke_sessions result=%+v err=%v revoked=%v, want no sessions revoked", res, err, f.revoker.keys)
	}

	req := setLoginPasswordRequest("exec-set-pw-7b", u.ID, "fresh")
	req.RevokeSessions = true
	res, err := f.svc.SetLoginPassword(ctx, req)
	if err != nil {
		t.Fatalf("SetLoginPassword with revoke_sessions: %v", err)
	}
	if len(f.revoker.keys) != 2 || res.Details["revoked_count"] != 2 {
		t.Fatalf("revoked keys=%v details=%+v, want both authorizations revoked", f.revoker.keys, res.Details)
	}
	if err := f.signIn(t, "+15550088007", f.challenge(t, "+15550088007"), "fresh"); err != nil {
		t.Fatalf("sign in after the reset: %v", err)
	}
}

func TestSetLoginPasswordNeverRecordsThePassword(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088008", FirstName: "Audit"})
	const password = "Tr0ub4dor&3-unique-marker"

	dry := setLoginPasswordRequest("dry-set-pw-8", u.ID, password)
	dry.DryRun = true
	var results []CommandResult
	for _, req := range []SetLoginPasswordRequest{dry, setLoginPasswordRequest("exec-set-pw-8", u.ID, password)} {
		res, err := f.svc.SetLoginPassword(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.CommandID, err)
		}
		results = append(results, res)
	}
	// 不带密钥的摘要（任何人拿候选密码都能离线算出来）同样不能出现。
	sum := sha256.Sum256([]byte(password))
	forbidden := []string{password, hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:])}
	for _, id := range []string{"dry-set-pw-8", "exec-set-pw-8"} {
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

	// 指纹由服务端密钥决定：换一把密钥，同一个密码的指纹就不同。
	otherRepo := newMemoryCommandRepo()
	other := NewService(Dependencies{Commands: otherRepo, LoginPasswords: f.svc.loginPasswords,
		LoginPasswordFingerprintKey: DeriveLoginPasswordFingerprintKey("another-token"), Now: fixedNow})
	if _, err := other.SetLoginPassword(ctx, dry); err != nil {
		t.Fatalf("dry run with another key: %v", err)
	}
	if storedFingerprint(t, otherRepo.items[dry.CommandID]) == storedFingerprint(t, f.repo.items[dry.CommandID]) {
		t.Fatal("the fingerprint does not depend on the server key")
	}
}

func TestSetLoginPasswordRequiresAFingerprintKey(t *testing.T) {
	f := newLoginPasswordFixture(t)
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), LoginPasswords: f.svc.loginPasswords, Now: fixedNow})
	u := f.user(t, domain.User{Phone: "15550088009", FirstName: "NoKey"})
	if _, err := svc.SetLoginPassword(context.Background(), setLoginPasswordRequest("exec-set-pw-9", u.ID, "secret")); err == nil {
		t.Fatal("SetLoginPassword without a fingerprint key succeeded")
	}
	if _, has, _ := f.passwords.GetLoginPassword(context.Background(), u.ID); has {
		t.Fatal("a password was stored without a fingerprint key")
	}
}
