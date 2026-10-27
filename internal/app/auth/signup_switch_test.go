package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// signUpSwitch 是测试用的注册开关：运行时可切换，模拟运营者在流程中途关闭注册。
type signUpSwitch struct{ disabled atomic.Bool }

func (s *signUpSwitch) allowed(context.Context) (bool, error) { return !s.disabled.Load(), nil }
func (s *signUpSwitch) set(enabled bool)                      { s.disabled.Store(!enabled) }

type signUpSwitchFixture struct {
	svc    *Service
	users  *memory.UserStore
	codes  *memory.CodeStore
	sender *captureOTPSender
	sw     *signUpSwitch
}

func newSignUpSwitchFixture(t *testing.T, opts ...Option) signUpSwitchFixture {
	t.Helper()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	f := signUpSwitchFixture{
		users:  memory.NewUserStore(),
		codes:  memory.NewCodeStore(),
		sender: &captureOTPSender{},
		sw:     &signUpSwitch{},
	}
	opts = append([]Option{
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
		WithPasswords(memory.NewPasswordStore()),
		WithPhoneCodeDelivery(f.sender, 5),
		WithSignUpAllowed(f.sw.allowed),
	}, opts...)
	f.svc = NewService(f.users, memory.NewAuthorizationStore(), f.codes, nil, nil, "12345", opts...)
	return f
}

func (f signUpSwitchFixture) existingUser(t *testing.T, phone string) domain.User {
	t.Helper()
	u, err := f.users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Existing"})
	if err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	return u
}

// lastCode 返回最近一次投递给 provider 的验证码。
func (f signUpSwitchFixture) lastCode(t *testing.T) string {
	t.Helper()
	if len(f.sender.requests) == 0 {
		t.Fatal("no code was delivered")
	}
	return f.sender.requests[len(f.sender.requests)-1].Code
}

func TestSignUpDisabledSendCodeRejectsNewNumberWithoutIssuingOrDelivering(t *testing.T) {
	f := newSignUpSwitchFixture(t)
	f.sw.set(false)

	hash, err := f.svc.SendCode(context.Background(), "+15550007501")
	if !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("SendCode err=%v hash=%q, want ErrSignUpDisabled", err, hash)
	}
	if len(f.sender.requests) != 0 {
		t.Fatalf("SMS deliveries=%d, want none for a rejected new number", len(f.sender.requests))
	}
}

func TestSignUpDisabledAfterSendCodeResendCodeRejectsNewNumber(t *testing.T) {
	ctx := context.Background()
	f := newSignUpSwitchFixture(t)
	phone := "+15550007502"
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while open: %v", err)
	}
	f.sw.set(false)
	before := len(f.sender.requests)

	if got, err := f.svc.ResendCode(ctx, phone, hash); !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("ResendCode err=%v hash=%q, want ErrSignUpDisabled", err, got)
	}
	if len(f.sender.requests) != before {
		t.Fatalf("SMS deliveries grew %d -> %d, want no resend for a new number", before, len(f.sender.requests))
	}
}

func TestSignUpDisabledAfterSendCodeSignInRejectsInsteadOfSignUpRequired(t *testing.T) {
	ctx := context.Background()
	f := newSignUpSwitchFixture(t)
	phone := "+15550007503"
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while open: %v", err)
	}
	f.sw.set(false)

	u, _, needSignUp, err := f.svc.SignIn(ctx, domain.Authorization{}, phone, hash, f.lastCode(t))
	if !errors.Is(err, domain.ErrSignUpDisabled) || needSignUp || u.ID != 0 {
		t.Fatalf("SignIn user=%+v needSignUp=%v err=%v, want ErrSignUpDisabled", u, needSignUp, err)
	}
}

func TestSignUpDisabledAfterSignInSignUpCreatesNoAccount(t *testing.T) {
	ctx := context.Background()
	f := newSignUpSwitchFixture(t)
	phone := "+15550007504"
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode while open: %v", err)
	}
	verifyCodeForSignUp(t, f.svc, phone, hash, f.lastCode(t))
	f.sw.set(false)

	var key [8]byte
	key[0] = 0x75
	if u, _, err := f.svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, "Late", "Closed"); !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("SignUp user=%+v err=%v, want ErrSignUpDisabled", u, err)
	}
	if _, found, err := f.users.ByPhone(ctx, domain.NormalizePhone(phone)); err != nil || found {
		t.Fatalf("account after rejected SignUp found=%v err=%v, want none", found, err)
	}
	// 重新开放后，同一份已验证的码仍可完成注册：拒绝不应消费它。
	f.sw.set(true)
	if _, _, err := f.svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, "Reopened", "Signup"); err != nil {
		t.Fatalf("SignUp after reopening: %v", err)
	}
}

// noLoginEmails 是没有任何登录邮箱的 loginEmailStore：新号码在 RequireSetup 下
// 会走邮箱设置注册。
type noLoginEmails struct{}

func (noLoginEmails) LoginEmailByPhone(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (noLoginEmails) SetLoginEmail(context.Context, int64, string) error { return nil }

func TestSignUpDisabledEmailSetupSignUpRejectedAtSendCode(t *testing.T) {
	f := newSignUpSwitchFixture(t, WithLoginEmail(LoginEmailOptions{
		Enabled:      true,
		RequireSetup: true,
		CodeLength:   6,
		Store:        noLoginEmails{},
		Sender:       &captureOTPSender{},
	}))
	f.sw.set(false)

	if hash, err := f.svc.SendCode(context.Background(), "+15550007505"); !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("SendCode email-setup err=%v hash=%q, want ErrSignUpDisabled", err, hash)
	}
}

func TestSignUpDisabledExistingAccountCodeLoginUnaffected(t *testing.T) {
	ctx := context.Background()
	f := newSignUpSwitchFixture(t)
	phone := "15550007506"
	existing := f.existingUser(t, phone)
	f.sw.set(false)

	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode existing: %v", err)
	}
	hash, err = f.svc.ResendCode(ctx, phone, hash)
	if err != nil {
		t.Fatalf("ResendCode existing: %v", err)
	}
	var key [8]byte
	key[0] = 0x76
	u, _, needSignUp, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, f.lastCode(t))
	if err != nil || needSignUp || u.ID != existing.ID {
		t.Fatalf("SignIn existing user=%d needSignUp=%v err=%v, want %d", u.ID, needSignUp, err, existing.ID)
	}
}

func TestSignUpDisabledExistingTwoFactorAndQRLoginUnaffected(t *testing.T) {
	ctx := context.Background()
	passwords := memory.NewPasswordStore()
	f := newSignUpSwitchFixture(t, WithPasswords(passwords))
	phone := "15550007507"
	existing := f.existingUser(t, phone)
	if err := passwords.Save(ctx, existing.ID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("save password: %v", err)
	}
	f.sw.set(false)

	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode existing 2FA: %v", err)
	}
	var key [8]byte
	key[0] = 0x77
	if _, _, _, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, f.lastCode(t)); !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		t.Fatalf("SignIn 2FA err=%v, want ErrSessionPasswordNeeded", err)
	}
	if err := f.svc.CompletePasswordSignIn(ctx, key, existing.ID); err != nil {
		t.Fatalf("CompletePasswordSignIn: %v", err)
	}

	var qrKey [8]byte
	qrKey[0] = 0x78
	if _, err := f.svc.AcceptLoginToken(ctx, domain.Authorization{AuthKeyID: qrKey}, existing.ID); err != nil {
		t.Fatalf("AcceptLoginToken while sign-up disabled: %v", err)
	}
}

func TestSignUpOpenByDefaultAndWhenSwitchedBackOn(t *testing.T) {
	ctx := context.Background()
	// 未配置开关来源：行为与之前一致，新号码可以注册。
	plain := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	if allowed, err := plain.SignUpAllowed(ctx); err != nil || !allowed {
		t.Fatalf("default SignUpAllowed=%v err=%v, want true", allowed, err)
	}
	hash, err := plain.SendCode(ctx, "15550007508")
	if err != nil {
		t.Fatalf("default SendCode new number: %v", err)
	}
	verifyCodeForSignUp(t, plain, "15550007508", hash, "12345")

	f := newSignUpSwitchFixture(t)
	f.sw.set(false)
	if allowed, _ := f.svc.SignUpAllowed(ctx); allowed {
		t.Fatal("SignUpAllowed=true while switch is off")
	}
	f.sw.set(true)
	if _, err := f.svc.SendCode(ctx, "15550007509"); err != nil {
		t.Fatalf("SendCode after switching back on: %v", err)
	}
}

func TestSignUpSwitchSourceErrorFailsClosed(t *testing.T) {
	boom := errors.New("identity store unreadable")
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithSignUpAllowed(func(context.Context) (bool, error) { return false, boom }))
	if _, err := svc.SendCode(context.Background(), "15550007510"); !errors.Is(err, boom) {
		t.Fatalf("SendCode with failing switch err=%v, want wrapped source error", err)
	}
}

func TestStaticSignUpAllowedReflectsEnvDefault(t *testing.T) {
	ctx := context.Background()
	for _, enabled := range []bool{true, false} {
		svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
			WithSignUpAllowed(StaticSignUpAllowed(enabled)))
		_, err := svc.SendCode(ctx, "15550007511")
		if enabled && err != nil {
			t.Fatalf("enabled SendCode: %v", err)
		}
		if !enabled && !errors.Is(err, domain.ErrSignUpDisabled) {
			t.Fatalf("disabled SendCode err=%v, want ErrSignUpDisabled", err)
		}
	}
}

// 密码注册（SignUpWithAccountCreator）同样经过唯一的注册开关判断：关闭时不调用 create。
func TestSignUpDisabledSignUpWithAccountCreatorCreatesNoAccount(t *testing.T) {
	f := newSignUpSwitchFixture(t)
	f.sw.set(false)
	created := false
	_, err := f.svc.SignUpWithAccountCreator(context.Background(), domain.Authorization{}, "+15550007560", "New", "",
		func(ctx context.Context, u domain.User, _ *domain.AccountPermissions) (domain.User, error) {
			created = true
			return f.users.Create(ctx, u)
		})
	if !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("SignUpWithAccountCreator err=%v, want ErrSignUpDisabled", err)
	}
	if created {
		t.Fatal("account creator ran although sign-up is closed")
	}
}
