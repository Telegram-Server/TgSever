package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	accountapp "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// codeLoginSwitch 是测试用的验证码登录开关：运行时可切换，模拟运营者在流程中途关闭验证码登录。
type codeLoginSwitch struct{ disabled atomic.Bool }

func (s *codeLoginSwitch) allowed(context.Context) (bool, error) { return !s.disabled.Load(), nil }
func (s *codeLoginSwitch) set(enabled bool)                      { s.disabled.Store(!enabled) }

type codeLoginSwitchFixture struct {
	signUpSwitchFixture
	codeLogin *codeLoginSwitch
}

func newCodeLoginSwitchFixture(t *testing.T, opts ...Option) codeLoginSwitchFixture {
	t.Helper()
	sw := &codeLoginSwitch{}
	f := newSignUpSwitchFixture(t, append([]Option{WithCodeLoginAllowed(sw.allowed)}, opts...)...)
	return codeLoginSwitchFixture{signUpSwitchFixture: f, codeLogin: sw}
}

func TestCodeLoginDisabledSendCodeRejectsWithoutIssuingOrDelivering(t *testing.T) {
	f := newCodeLoginSwitchFixture(t)
	f.existingUser(t, "15550009101")
	f.codeLogin.set(false)

	for _, phone := range []string{"15550009101", "15550009102"} {
		hash, err := f.svc.SendCode(context.Background(), phone)
		if !errors.Is(err, domain.ErrCodeLoginDisabled) {
			t.Fatalf("SendCode(%s) err=%v hash=%q, want ErrCodeLoginDisabled", phone, err, hash)
		}
	}
	if len(f.sender.requests) != 0 {
		t.Fatalf("deliveries=%d, want none while code login is disabled", len(f.sender.requests))
	}
}

func TestCodeLoginDisabledAfterSendCodeResendCodeRejectsWithoutDelivering(t *testing.T) {
	ctx := context.Background()
	f := newCodeLoginSwitchFixture(t)
	phone := "15550009103"
	f.existingUser(t, phone)
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while enabled: %v", err)
	}
	f.codeLogin.set(false)
	before := len(f.sender.requests)

	if got, err := f.svc.ResendCode(ctx, phone, hash); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("ResendCode err=%v hash=%q, want ErrCodeLoginDisabled", err, got)
	}
	if len(f.sender.requests) != before {
		t.Fatalf("deliveries grew %d -> %d, want no resend", before, len(f.sender.requests))
	}
}

func TestCodeLoginDisabledAfterSendCodeSignInRejectsIssuedPhoneCode(t *testing.T) {
	ctx := context.Background()
	f := newCodeLoginSwitchFixture(t)
	phone := "15550009104"
	f.existingUser(t, phone)
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while enabled: %v", err)
	}
	f.codeLogin.set(false)

	var key [8]byte
	key[0] = 0x91
	u, _, needSignUp, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, f.lastCode(t))
	if !errors.Is(err, domain.ErrCodeLoginDisabled) || needSignUp || u.ID != 0 {
		t.Fatalf("SignIn user=%d needSignUp=%v err=%v, want ErrCodeLoginDisabled", u.ID, needSignUp, err)
	}
	// 重新开启后同一份码仍可登录：拒绝不应消费它。
	f.codeLogin.set(true)
	if u, _, _, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, f.lastCode(t)); err != nil || u.ID == 0 {
		t.Fatalf("SignIn after re-enabling user=%d err=%v", u.ID, err)
	}
}

func TestCodeLoginDisabledAfterSendCodeSignInRejectsIssuedEmailCode(t *testing.T) {
	ctx := context.Background()
	phone := "15550009105"
	mail := &testMailSender{}
	f := newCodeLoginSwitchFixture(t, WithLoginEmail(LoginEmailOptions{
		Enabled:    true,
		CodeLength: 6,
		Store:      &testLoginEmailStore{emails: map[string]string{phone: "owner@example.test"}},
		Sender:     mail,
	}))
	f.existingUser(t, phone)
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while enabled: %v", err)
	}
	if mail.code == "" {
		t.Fatal("no email login code was delivered")
	}
	f.codeLogin.set(false)

	var key [8]byte
	key[0] = 0x92
	if u, _, _, err := f.svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, mail.code); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SignInWithEmail user=%d err=%v, want ErrCodeLoginDisabled", u.ID, err)
	}
	if u, _, _, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, mail.code); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SignIn with email code user=%d err=%v, want ErrCodeLoginDisabled", u.ID, err)
	}
}

func TestCodeLoginDisabledAfterSignInSignUpCreatesNoAccount(t *testing.T) {
	ctx := context.Background()
	f := newCodeLoginSwitchFixture(t)
	phone := "15550009106"
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while enabled: %v", err)
	}
	verifyCodeForSignUp(t, f.svc, phone, hash, f.lastCode(t))
	f.codeLogin.set(false)

	var key [8]byte
	key[0] = 0x93
	if u, _, err := f.svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, "Late", "Closed"); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SignUp user=%+v err=%v, want ErrCodeLoginDisabled", u, err)
	}
	if _, found, err := f.users.ByPhone(ctx, domain.NormalizePhone(phone)); err != nil || found {
		t.Fatalf("account after rejected SignUp found=%v err=%v, want none", found, err)
	}
}

// 检查顺序：验证码登录开关先于注册开关。两者都关闭时，新号码也只返回 CODE_LOGIN_DISABLED。
func TestCodeLoginDisabledTakesPrecedenceOverSignUpDisabled(t *testing.T) {
	ctx := context.Background()
	f := newCodeLoginSwitchFixture(t)
	phone := "15550009107"
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while enabled: %v", err)
	}
	f.sw.set(false)
	f.codeLogin.set(false)

	if _, err := f.svc.SendCode(ctx, phone); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SendCode err=%v, want ErrCodeLoginDisabled", err)
	}
	if _, err := f.svc.ResendCode(ctx, phone, hash); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("ResendCode err=%v, want ErrCodeLoginDisabled", err)
	}
	if _, _, _, err := f.svc.SignIn(ctx, domain.Authorization{}, phone, hash, f.lastCode(t)); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SignIn err=%v, want ErrCodeLoginDisabled", err)
	}
}

// 不受影响的入口：密码登录与密码注册、2FA 收尾、扫码登录、会话内更换手机号重发。
func TestCodeLoginDisabledLeavesNonCodeEntriesWorking(t *testing.T) {
	ctx := context.Background()
	passwords := memory.NewPasswordStore()
	f := newCodeLoginSwitchFixture(t, WithPasswords(passwords))
	existing := f.existingUser(t, "15550009109")
	f.codeLogin.set(false)

	// 密码登录（loginpassword 验证 SRP 之后调用 FinishVerifiedSignIn）。
	var key [8]byte
	key[0] = 0x94
	if u, err := f.svc.FinishVerifiedSignIn(ctx, domain.Authorization{AuthKeyID: key}, existing); err != nil || u.ID != existing.ID {
		t.Fatalf("FinishVerifiedSignIn user=%d err=%v, want %d", u.ID, err, existing.ID)
	}
	// 密码注册。
	key[0] = 0x95
	created, err := f.svc.SignUpWithAccountCreator(ctx, domain.Authorization{AuthKeyID: key}, "15550009110", "Password", "",
		func(ctx context.Context, u domain.User) (domain.User, error) { return f.users.Create(ctx, u) })
	if err != nil || created.ID == 0 {
		t.Fatalf("SignUpWithAccountCreator user=%d err=%v", created.ID, err)
	}
	// auth.checkPassword 的收尾：2FA 账号经密码登录后停在 password_pending。
	if err := passwords.Save(ctx, existing.ID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("save 2FA password: %v", err)
	}
	key[0] = 0x96
	if _, err := f.svc.FinishVerifiedSignIn(ctx, domain.Authorization{AuthKeyID: key}, existing); !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		t.Fatalf("FinishVerifiedSignIn 2FA err=%v, want ErrSessionPasswordNeeded", err)
	}
	if err := f.svc.CompletePasswordSignIn(ctx, key, existing.ID); err != nil {
		t.Fatalf("CompletePasswordSignIn: %v", err)
	}
	// 扫码登录。
	var qrKey [8]byte
	qrKey[0] = 0x97
	if _, err := f.svc.AcceptLoginToken(ctx, domain.Authorization{AuthKeyID: qrKey}, existing.ID); err != nil {
		t.Fatalf("AcceptLoginToken: %v", err)
	}
	// 会话内更换手机号的验证码重发。
	authKeyID := [8]byte{9, 1, 0}
	if err := f.codes.Set(ctx, "change-phone-hash", store.PhoneCode{
		Version:     store.PhoneCodeVersionCurrent,
		Phone:       "15550009111",
		Code:        "old",
		Channel:     codeChannelPhone,
		Purpose:     store.PhoneCodePurposeChangePhone,
		UserID:      existing.ID,
		AuthKeyID:   authKeyID,
		MaxAttempts: 5,
	}, time.Minute); err != nil {
		t.Fatalf("seed change-phone code: %v", err)
	}
	if hash, err := f.svc.ResendCodeForAuthKey(ctx, authKeyID, "15550009111", "change-phone-hash"); err != nil || hash == "" {
		t.Fatalf("ResendCodeForAuthKey change-phone hash=%q err=%v", hash, err)
	}
}

func TestCodeLoginAllowedByDefaultAndWhenSwitchedBackOn(t *testing.T) {
	ctx := context.Background()
	// 未配置开关来源：行为与之前一致。
	plain := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	if allowed, err := plain.CodeLoginAllowed(ctx); err != nil || !allowed {
		t.Fatalf("default CodeLoginAllowed=%v err=%v, want true", allowed, err)
	}
	if _, err := plain.SendCode(ctx, "15550009112"); err != nil {
		t.Fatalf("default SendCode: %v", err)
	}

	f := newCodeLoginSwitchFixture(t)
	f.codeLogin.set(false)
	if allowed, _ := f.svc.CodeLoginAllowed(ctx); allowed {
		t.Fatal("CodeLoginAllowed=true while switch is off")
	}
	f.codeLogin.set(true)
	if _, err := f.svc.SendCode(ctx, "15550009113"); err != nil {
		t.Fatalf("SendCode after switching back on: %v", err)
	}
}

func TestStaticCodeLoginAllowedReflectsEnvDefault(t *testing.T) {
	ctx := context.Background()
	for _, enabled := range []bool{true, false} {
		svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
			WithCodeLoginAllowed(StaticCodeLoginAllowed(enabled)))
		_, err := svc.SendCode(ctx, "15550009114")
		if enabled && err != nil {
			t.Fatalf("enabled SendCode: %v", err)
		}
		if !enabled && !errors.Is(err, domain.ErrCodeLoginDisabled) {
			t.Fatalf("disabled SendCode err=%v, want ErrCodeLoginDisabled", err)
		}
	}
}

func TestCodeLoginSwitchSourceErrorFailsClosed(t *testing.T) {
	boom := errors.New("identity store unreadable")
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithCodeLoginAllowed(func(context.Context) (bool, error) { return false, boom }))
	if _, err := svc.SendCode(context.Background(), "15550009115"); !errors.Is(err, boom) {
		t.Fatalf("SendCode with failing switch err=%v, want wrapped source error", err)
	}
}

// 登录邮箱 setup（account.sendVerifyEmailCode / verifyEmail 的 loginSetup）同样经过
// 唯一的验证码登录判断：签发之后才关闭时，既不再发邮箱码，也不接受已发出的邮箱码。
func TestCodeLoginDisabledLoginEmailSetupRejected(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	sender := &testMailSender{}
	sw := &codeLoginSwitch{}
	// 与 cmd/telesrv 的接线相同：account 与 auth 共用同一个 CodeLoginAllowedFunc。
	accountSvc := accountapp.NewService(memory.NewPasswordStore(),
		accountapp.WithUsers(users),
		accountapp.WithLoginEmailVerification(codes, sender, time.Minute, 3, 6),
		accountapp.WithCodeLoginGate(CodeLoginGate(sw.allowed)),
	)
	authSvc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithCodeLoginAllowed(sw.allowed),
		WithLoginEmail(LoginEmailOptions{
			Enabled:      true,
			RequireSetup: true,
			CodeLength:   6,
			Store:        accountSvc,
			Sender:       sender,
		}),
	)
	phone := "15550009116"
	hash, err := authSvc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while enabled: %v", err)
	}
	if _, _, err := accountSvc.SendLoginEmailCode(ctx, 0, phone, hash, "setup@example.test", true); err != nil {
		t.Fatalf("SendLoginEmailCode setup while enabled: %v", err)
	}
	issued := sender.code
	sw.set(false)
	sender.code = ""

	if _, _, err := accountSvc.SendLoginEmailCode(ctx, 0, phone, hash, "setup@example.test", true); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SendLoginEmailCode setup err=%v, want ErrCodeLoginDisabled", err)
	}
	if sender.code != "" {
		t.Fatal("email setup code delivered while code login is disabled")
	}
	if _, err := accountSvc.VerifyLoginEmail(ctx, 0, phone, hash, issued, true); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("VerifyLoginEmail setup err=%v, want ErrCodeLoginDisabled", err)
	}
	// 拒绝不消费已发出的邮箱码：重新开启后仍可完成验证。
	sw.set(true)
	if _, err := accountSvc.VerifyLoginEmail(ctx, 0, phone, hash, issued, true); err != nil {
		t.Fatalf("VerifyLoginEmail setup after re-enabling: %v", err)
	}
}

func TestCodeLoginDisabledLoginEmailResetRejected(t *testing.T) {
	ctx := context.Background()
	f := newCodeLoginSwitchFixture(t)
	owner := f.existingUser(t, "15550009108")
	if err := f.codes.Set(ctx, "email-login-hash", store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: owner.ID,
		Phone:        owner.Phone,
		Code:         "654321",
		Channel:      codeChannelEmailLogin,
		MaxAttempts:  5,
	}, time.Minute); err != nil {
		t.Fatalf("seed email login code: %v", err)
	}
	f.codeLogin.set(false)

	if _, err := f.svc.ConsumeLoginEmailReset(ctx, owner.Phone, "email-login-hash"); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("ConsumeLoginEmailReset err=%v, want ErrCodeLoginDisabled", err)
	}
	if _, found, _ := f.codes.Get(ctx, "email-login-hash"); !found {
		t.Fatal("rejected reset consumed the email login hash")
	}
	if _, err := f.svc.SendPhoneCodeAfterLoginEmailReset(ctx, owner.Phone, owner.ID); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SendPhoneCodeAfterLoginEmailReset err=%v, want ErrCodeLoginDisabled", err)
	}
	if len(f.sender.requests) != 0 {
		t.Fatalf("deliveries=%d, want none", len(f.sender.requests))
	}
}
