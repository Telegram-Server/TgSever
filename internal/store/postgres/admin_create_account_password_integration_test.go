package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	tdsrp "github.com/iamxvbaba/td/crypto/srp"

	"telesrv/internal/admin"
	appauth "telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// signedInFlows 是登录收尾的替身：证明成立就算登录成功，这里只关心密码对不对。
type signedInFlows struct{}

func (signedInFlows) CheckSignUpAllowed(context.Context) error { return nil }

func (signedInFlows) SignUpWithAccountCreator(ctx context.Context, _ domain.Authorization, phone, firstName, lastName string, create appauth.CreateAccountFunc) (domain.User, error) {
	return create(ctx, domain.User{Phone: phone, FirstName: firstName, LastName: lastName})
}

func (signedInFlows) FinishVerifiedSignIn(_ context.Context, _ domain.Authorization, u domain.User) (domain.User, error) {
	return u, nil
}

// brokenPasswordAccounts 把要写入的登录密码弄坏（去掉 verifier），让真实存储在建号事务里写密码
// 这一步失败，用来验证事务回滚之后账号也不在。
type brokenPasswordAccounts struct {
	*AccountCreationStore
}

func (s brokenPasswordAccounts) CreateAccount(ctx context.Context, u domain.User, extras store.AccountExtras) (domain.User, error) {
	if extras.LoginPassword != nil {
		broken := *extras.LoginPassword
		broken.Verifier = nil
		extras.LoginPassword = &broken
	}
	return s.AccountCreationStore.CreateAccount(ctx, u, extras)
}

// TestAdminCreateAccountWithPasswordPostgres 后台建号带登录密码在真实存储上：建好的账号马上能用
// 这个密码登录（证明由 td 客户端的 SRP 实现算出）；号码冲突或写密码失败时什么都不留下；
// 命令表和审计表里没有明文密码，也没有不带密钥的摘要。
func TestAdminCreateAccountWithPasswordPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	passwords := NewLoginPasswordStore(pool)
	challenges := memory.NewLoginPasswordChallengeStore(time.Now)
	authSvc := appauth.NewService(users, NewAuthorizationStore(pool), memory.NewCodeStore(), nil, nil, "12345",
		appauth.WithSignUpAllowed(appauth.StaticSignUpAllowed(false)))
	accountCreation := NewAccountCreationStore(pool)
	newAdmin := func(accounts store.AccountCreationStore) *admin.Service {
		return admin.NewService(admin.Dependencies{
			Commands:                    NewAdminStore(pool),
			AccountCreator:              authSvc,
			LoginPasswords:              loginpassword.NewService(users, passwords, accounts, challenges, nil),
			LoginPasswordFingerprintKey: admin.DeriveLoginPasswordFingerprintKey("pg-admin-token"),
		})
	}
	svc := newAdmin(accountCreation)
	login := loginpassword.NewService(users, passwords, accountCreation, challenges, signedInFlows{}, loginpassword.WithEnabled(true))
	const phone = "15550090041"
	const password = "pg-create-plaintext-marker-2c8d"

	req := admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "exec-pg-create-account-pw", Actor: "ops", Reason: "new hire"},
		Phone:       "+" + phone, FirstName: "Ivy", Password: password,
	}
	res, err := svc.CreateAccount(ctx, req)
	if err != nil || res.Status != "completed" || res.Details["login_password"] != true {
		t.Fatalf("create with a password = %+v, %v", res, err)
	}
	authKey := [8]byte{4, 4, 4, 4}
	settings, err := login.GetLoginPassword(ctx, authKey, "+"+phone)
	if err != nil || !settings.HasPassword {
		t.Fatalf("GetLoginPassword = %+v, %v; want a login challenge", settings, err)
	}
	a := make([]byte, srp.HashSize)
	if _, err := rand.Read(a); err != nil {
		t.Fatal(err)
	}
	algo := settings.CurrentAlgo
	answer, err := tdsrp.NewSRP(nil).Hash([]byte(password), settings.SRPB, a, tdsrp.Input{Salt1: algo.Salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P})
	if err != nil {
		t.Fatalf("td Hash: %v", err)
	}
	if _, err := login.SignIn(ctx, domain.Authorization{}, authKey, "+"+phone, domain.PasswordCheck{SRPID: settings.SRPID, A: answer.A, M1: answer.M1}); err != nil {
		t.Fatalf("sign in with the password set at creation: %v", err)
	}

	// 号码已被占用：失败，已有账号的密码也不被改动。
	existing, _, _ := users.ByPhone(ctx, phone)
	before, _, _ := passwords.GetLoginPassword(ctx, existing.ID)
	dup := req
	dup.CommandID = "dup-pg-create-account-pw"
	dup.Password = "another password"
	if res, err := svc.CreateAccount(ctx, dup); !errors.Is(err, domain.ErrPhoneNumberOccupied) || res.Code != "PHONE_NUMBER_OCCUPIED" {
		t.Fatalf("occupied number = %+v, %v; want PHONE_NUMBER_OCCUPIED", res, err)
	}
	if after, _, _ := passwords.GetLoginPassword(ctx, existing.ID); string(after.Verifier) != string(before.Verifier) {
		t.Fatal("a rejected creation changed the existing account's login password")
	}

	// 写密码失败：整个事务回滚，号码上没有账号。
	const brokenPhone = "15550090042"
	broken := newAdmin(brokenPasswordAccounts{accountCreation})
	if res, err := broken.CreateAccount(ctx, admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "broken-pg-create-account-pw", Actor: "ops", Reason: "new hire"},
		Phone:       brokenPhone, FirstName: "Half", Password: password,
	}); err == nil || res.Status != "failed" {
		t.Fatalf("failing password write = %+v, %v; want a failed command", res, err)
	}
	var accounts int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM users WHERE phone = $1`, brokenPhone).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("accounts after a failed password write=%d err=%v, want none", accounts, err)
	}

	sum := sha256.Sum256([]byte(password))
	for _, id := range []string{req.CommandID, dup.CommandID, "broken-pg-create-account-pw"} {
		var commandRow, auditRow string
		if err := pool.QueryRow(ctx, `SELECT request::text || result::text || coalesce(error, '') FROM admin_commands WHERE command_id = $1`, id).Scan(&commandRow); err != nil {
			t.Fatalf("read command %s: %v", id, err)
		}
		if err := pool.QueryRow(ctx, `SELECT action || request::text || result::text FROM admin_audit_logs WHERE command_id = $1`, id).Scan(&auditRow); err != nil {
			t.Fatalf("read audit %s: %v", id, err)
		}
		if !strings.HasPrefix(auditRow, admin.ActionCreateAccount) || !strings.Contains(commandRow, "password_fingerprint") {
			t.Fatalf("%s audit=%s command=%s, want %s with a password fingerprint", id, auditRow, commandRow, admin.ActionCreateAccount)
		}
		for _, row := range []string{commandRow, auditRow} {
			if strings.Contains(row, password) || strings.Contains(row, hex.EncodeToString(sum[:])) {
				t.Fatalf("%s recorded the password or an unkeyed digest: %s", id, row)
			}
		}
	}
}
