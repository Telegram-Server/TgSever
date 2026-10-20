package loginpassword_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	tdsrp "github.com/iamxvbaba/td/crypto/srp"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store/memory"
)

// 业务层单测只覆盖端到端测试里构造不出来的情形：challenge 过期（注入时钟，不 sleep）、
// 期间密码被改（#34 之前没有修改密码的 RPC）。bot/系统账号的拒绝在 auth.FinishVerifiedSignIn
// 里（见 internal/app/auth/finish_verified_sign_in_test.go）。其余行为以
// internal/mtprotoedge/login_password_e2e_test.go 为准。

var testAuthKey = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

// fakeFlows 记录登录收尾是否被调用；注册路径直接建号。注册开关的判断交给 gate
// （真实的 auth.Service，nil 表示开放注册），不另写一套。
type fakeFlows struct {
	mu       sync.Mutex
	signedIn []int64
	gate     *auth.Service
}

func (f *fakeFlows) CheckSignUpAllowed(ctx context.Context) error {
	return f.gate.CheckSignUpAllowed(ctx)
}

func (f *fakeFlows) SignUpWithAccountCreator(ctx context.Context, _ domain.Authorization, phone, firstName, lastName string, create auth.CreateAccountFunc) (domain.User, error) {
	return create(ctx, domain.User{Phone: phone, FirstName: firstName, LastName: lastName})
}

func (f *fakeFlows) FinishVerifiedSignIn(_ context.Context, _ domain.Authorization, u domain.User) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signedIn = append(f.signedIn, u.ID)
	return u, nil
}

func (f *fakeFlows) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.signedIn)
}

type fixture struct {
	svc       *loginpassword.Service
	users     *memory.UserStore
	passwords *memory.LoginPasswordStore
	flows     *fakeFlows
	now       time.Time
	mu        sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{users: memory.NewUserStore(), flows: &fakeFlows{}, now: time.Unix(1_800_000_000, 0)}
	f.passwords = memory.NewLoginPasswordStore(f.users)
	f.svc = loginpassword.NewService(f.users, f.passwords, memory.NewLoginPasswordChallengeStore(f.clock), f.flows,
		loginpassword.WithEnabled(true), loginpassword.WithClock(f.clock))
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// newPassword 按客户端设置密码的方式（td 的 NewHash）算出要存的登录密码。
func newPassword(t *testing.T, password string) domain.LoginPassword {
	t.Helper()
	algo := srp.DefaultAlgo()
	hash, salt1, err := tdsrp.NewSRP(rand.Reader).NewHash([]byte(password), tdsrp.Input{Salt1: algo.Salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P})
	if err != nil {
		t.Fatalf("td NewHash: %v", err)
	}
	verifier, err := srp.Pad(hash)
	if err != nil {
		t.Fatalf("Pad: %v", err)
	}
	return domain.LoginPassword{Salt1: salt1, Salt2: algo.Salt2, Verifier: verifier}
}

// proof 按客户端校验密码的方式（td 的 Hash）根据 getLoginPassword 的返回算出证明。
func proof(t *testing.T, settings domain.PasswordSettings, password string) domain.PasswordCheck {
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
	return domain.PasswordCheck{SRPID: settings.SRPID, A: answer.A, M1: answer.M1}
}

func (f *fixture) account(t *testing.T, u domain.User, password string) domain.User {
	t.Helper()
	created, err := f.passwords.CreateUserWithLoginPassword(context.Background(), u, newPassword(t, password))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return created
}

func TestSignInRejectsAnExpiredChallenge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.account(t, domain.User{Phone: "15550320001", FirstName: "Late"}, "pw")

	settings, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+15550320001")
	if err != nil {
		t.Fatalf("GetLoginPassword: %v", err)
	}
	check := proof(t, settings, "pw")
	f.advance(5 * time.Minute)
	if _, err := f.svc.SignIn(ctx, domain.Authorization{}, testAuthKey, "+15550320001", check); !errors.Is(err, domain.ErrSRPIDInvalid) {
		t.Fatalf("SignIn after the TTL = %v, want ErrSRPIDInvalid", err)
	}

	// 未过期的 challenge 正常通过。
	settings, err = f.svc.GetLoginPassword(ctx, testAuthKey, "+15550320001")
	if err != nil {
		t.Fatalf("GetLoginPassword: %v", err)
	}
	check = proof(t, settings, "pw")
	f.advance(5*time.Minute - time.Second)
	if _, err := f.svc.SignIn(ctx, domain.Authorization{}, testAuthKey, "+15550320001", check); err != nil {
		t.Fatalf("SignIn just before the TTL = %v, want nil", err)
	}
	if f.flows.calls() != 1 {
		t.Fatalf("FinishVerifiedSignIn called %d times, want 1", f.flows.calls())
	}
}

func TestSignInRejectsAChallengeWhosePasswordChanged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.account(t, domain.User{Phone: "15550320002", FirstName: "Changed"}, "old")

	settings, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+15550320002")
	if err != nil {
		t.Fatalf("GetLoginPassword: %v", err)
	}
	check := proof(t, settings, "old")
	if err := f.passwords.SetLoginPassword(ctx, u.ID, newPassword(t, "new")); err != nil {
		t.Fatalf("SetLoginPassword: %v", err)
	}
	if _, err := f.svc.SignIn(ctx, domain.Authorization{}, testAuthKey, "+15550320002", check); !errors.Is(err, domain.ErrSRPPasswordChanged) {
		t.Fatalf("SignIn after the password changed = %v, want ErrSRPPasswordChanged", err)
	}

	// 密码被删除同样作废已发出的 challenge。
	settings, err = f.svc.GetLoginPassword(ctx, testAuthKey, "+15550320002")
	if err != nil {
		t.Fatalf("GetLoginPassword: %v", err)
	}
	check = proof(t, settings, "new")
	if _, err := f.passwords.DeleteLoginPassword(ctx, u.ID); err != nil {
		t.Fatalf("DeleteLoginPassword: %v", err)
	}
	if _, err := f.svc.SignIn(ctx, domain.Authorization{}, testAuthKey, "+15550320002", check); !errors.Is(err, domain.ErrSRPPasswordChanged) {
		t.Fatalf("SignIn after the password was deleted = %v, want ErrSRPPasswordChanged", err)
	}
	if _, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+15550320002"); !errors.Is(err, domain.ErrPasswordLoginNotSet) {
		t.Fatalf("GetLoginPassword after delete = %v, want ErrPasswordLoginNotSet", err)
	}
	if f.flows.calls() != 0 {
		t.Fatalf("FinishVerifiedSignIn called %d times, want 0", f.flows.calls())
	}
}

func TestGetLoginPasswordChallengeCarriesTheStoredSalts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	stored := newPassword(t, "salts")
	if _, err := f.passwords.CreateUserWithLoginPassword(ctx, domain.User{Phone: "15550320004", FirstName: "Salt"}, stored); err != nil {
		t.Fatalf("create: %v", err)
	}
	settings, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+15550320004")
	if err != nil {
		t.Fatalf("GetLoginPassword: %v", err)
	}
	if !settings.HasPassword || settings.CurrentAlgo == nil || settings.SRPID == 0 || len(settings.SRPB) != srp.HashSize {
		t.Fatalf("settings = %+v, want has_password with current_algo, srp_id and srp_B", settings)
	}
	if !bytes.Equal(settings.CurrentAlgo.Salt1, stored.Salt1) || !bytes.Equal(settings.CurrentAlgo.Salt2, stored.Salt2) {
		t.Fatal("current_algo salts differ from the stored login password")
	}
	if len(settings.SRPBSecret) != 0 {
		t.Fatal("GetLoginPassword leaked the server secret b")
	}
	// 没有原始 auth_key 无法绑定 challenge。
	if _, err := f.svc.GetLoginPassword(ctx, [8]byte{}, "+15550320004"); err == nil {
		t.Fatal("GetLoginPassword without an auth key succeeded")
	}
}
