package rpc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// deviceLimitAuthService 让验证码登录、Passkey 绑定、扫码确认都返回 bindErr（默认是包一层的
// domain.ErrDeviceLimitReached，模拟业务层带上下文的 wrap）。
type deviceLimitAuthService struct {
	*captureAuthService
	bindErr error
}

var (
	errWrappedDeviceLimitReached = fmt.Errorf("upsert authorization: %w", domain.ErrDeviceLimitReached)
	errWrappedDeviceIDRequired   = fmt.Errorf("upsert authorization: %w", domain.ErrDeviceIDRequired)
)

// deviceBindingRejections 是设备绑定规则的两种拒绝（#112）及其 rpc_error。
var deviceBindingRejections = []struct {
	name string
	err  error
	typ  string
}{
	{"limit reached", errWrappedDeviceLimitReached, "AUTHORIZATIONS_TOO_MUCH"},
	{"device id required", errWrappedDeviceIDRequired, "DEVICE_ID_REQUIRED"},
}

func newDeviceLimitAuthService(bindErr error) deviceLimitAuthService {
	return deviceLimitAuthService{captureAuthService: &captureAuthService{}, bindErr: bindErr}
}

func (s deviceLimitAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, s.bindErr
}

func (s deviceLimitAuthService) BindVerifiedLogin(context.Context, domain.Authorization, int64) (domain.User, error) {
	return domain.User{}, s.bindErr
}

func (s deviceLimitAuthService) AcceptLoginToken(context.Context, domain.Authorization, int64) (domain.Authorization, error) {
	return domain.Authorization{}, s.bindErr
}

// deviceLimitLoginPassword 让 telesrv.signInWithPassword 在 SRP 校验通过后的绑定一步返回该领域错误。
type deviceLimitLoginPassword struct {
	LoginPasswordService
	err error
}

func (deviceLimitLoginPassword) Enabled() bool { return true }
func (p deviceLimitLoginPassword) SignIn(context.Context, domain.Authorization, [8]byte, string, domain.PasswordCheck) (domain.User, error) {
	return domain.User{}, p.err
}

// verifiedPasskey 让任何 passkey 断言都验证为 userID。
type verifiedPasskey struct {
	PasskeyService
	userID int64
}

func (p verifiedPasskey) FinishLogin(context.Context, []byte, []byte, []byte, []byte, string) (int64, error) {
	return p.userID, nil
}

func assertRPCError(t *testing.T, method string, err error, code int, typ string) {
	t.Helper()
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Code != code || rpcErr.Type != typ {
		t.Fatalf("%s err=%v, want %d %s", method, err, code, typ)
	}
}

func TestDeviceBindingRejectionsMapOnEveryLoginEntry(t *testing.T) {
	for _, rejection := range deviceBindingRejections {
		t.Run(rejection.name, func(t *testing.T) {
			assertLoginEntriesMapDeviceBindingRejection(t, rejection.err, rejection.typ)
		})
	}
}

func assertLoginEntriesMapDeviceBindingRejection(t *testing.T, bindErr error, typ string) {
	const userID = int64(1000000107)
	r := New(Config{}, Deps{
		Auth:          newDeviceLimitAuthService(bindErr),
		LoginPassword: deviceLimitLoginPassword{err: bindErr},
		Passkey:       verifiedPasskey{userID: userID},
	}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()

	cases := []struct {
		method string
		call   func() error
	}{
		{"auth.signIn", func() error {
			_, err := r.onAuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "15550105201", PhoneCodeHash: "h", PhoneCode: "12345"})
			return err
		}},
		{"telesrv.signInWithPassword", func() error {
			_, err := r.onTelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: "15550105201", Password: &tg.InputCheckPasswordEmpty{}})
			return err
		}},
		{"auth.finishPasskeyLogin", func() error {
			_, err := r.onAuthFinishPasskeyLogin(ctx, &tg.AuthFinishPasskeyLoginRequest{Credential: tg.InputPasskeyCredentialPublicKey{
				ID: "Y3JlZA", RawID: "Y3JlZA",
				Response: &tg.InputPasskeyResponseLogin{ClientData: tg.DataJSON{Data: "{}"}, UserHandle: "u"},
			}})
			return err
		}},
		{"auth.acceptLoginToken", func() error {
			_, err := acceptLoginTokenFromOtherDevice(t, r, userID)
			return err
		}},
	}
	for _, tc := range cases {
		assertRPCError(t, tc.method, tc.call(), 400, typ)
	}
}

// twoFactorDeviceLimitAuthService 让 2FA 转正返回 err（#106、#112）。
type twoFactorDeviceLimitAuthService struct {
	*captureAuthService
	err error
}

func (s twoFactorDeviceLimitAuthService) CompletePasswordSignIn(context.Context, [8]byte, int64) error {
	return s.err
}

// acceptingPasswordAccountService 让任何 2FA 密码校验与密码找回都通过。
type acceptingPasswordAccountService struct{ AccountService }

func (acceptingPasswordAccountService) CheckPassword(context.Context, int64, domain.PasswordCheck) error {
	return nil
}

func (acceptingPasswordAccountService) RecoverPassword(context.Context, int64, string, *domain.PasswordInputSettings) error {
	return nil
}

func newTwoFactorDeviceLimitRouter(t *testing.T, err error) *Router {
	auth := twoFactorDeviceLimitAuthService{&captureAuthService{pendingPasswordUserID: 42, pendingPassword: true}, err}
	return New(Config{}, Deps{Auth: auth, Account: acceptingPasswordAccountService{}}, zaptest.NewLogger(t), clock.System)
}

func TestDeviceBindingRejectionOnCheckPasswordMaps(t *testing.T) {
	for _, rejection := range deviceBindingRejections {
		t.Run(rejection.name, func(t *testing.T) {
			r := newTwoFactorDeviceLimitRouter(t, rejection.err)
			_, err := r.onAuthCheckPassword(pendingPasswordContext(), &tg.InputCheckPasswordSRP{SRPID: 1, A: []byte{1}, M1: []byte{1}})
			assertRPCError(t, "auth.checkPassword", err, 400, rejection.typ)
		})
	}
}

func TestDeviceBindingRejectionOnRecoverPasswordMaps(t *testing.T) {
	for _, rejection := range deviceBindingRejections {
		t.Run(rejection.name, func(t *testing.T) {
			r := newTwoFactorDeviceLimitRouter(t, rejection.err)
			_, err := r.onAuthRecoverPassword(pendingPasswordContext(), &tg.AuthRecoverPasswordRequest{Code: "123456"})
			assertRPCError(t, "auth.recoverPassword", err, 400, rejection.typ)
		})
	}
}

// fullDeviceLimitAuthService 的预检返回 err（名额已满或缺 device_id）；其余行为同
// captureAuthService（绑定、转正都会成功），所以 RPC 只有在有副作用的步骤之前先做预检才会拒绝。
type fullDeviceLimitAuthService struct {
	*captureAuthService
	err error
}

func (s fullDeviceLimitAuthService) CheckDeviceLimit(context.Context, [8]byte, int64) error {
	return s.err
}

func (s fullDeviceLimitAuthService) CheckDeviceLimitForPhone(context.Context, [8]byte, string) error {
	return s.err
}

// recordingLoginEmailAccount 记录登录邮箱设置是否真的执行（会消耗邮箱码并把登录邮箱写到账号上）。
type recordingLoginEmailAccount struct {
	AccountService
	verifyCalls int
}

func (a *recordingLoginEmailAccount) VerifyLoginEmail(context.Context, int64, string, string, string, bool) (string, error) {
	a.verifyCalls++
	return "alice@example.test", nil
}

// 名额已满或缺 device_id 时 account.verifyEmail(loginSetup) 在消耗邮箱码、把登录邮箱写到账号上
// 之前就拒绝。
func TestDeviceBindingRejectionRefusesLoginEmailSetupBeforeSettingEmail(t *testing.T) {
	for _, rejection := range deviceBindingRejections {
		t.Run(rejection.name, func(t *testing.T) {
			authSvc := fullDeviceLimitAuthService{&captureAuthService{}, rejection.err}
			account := &recordingLoginEmailAccount{}
			r := New(Config{}, Deps{Auth: authSvc, Account: account}, zaptest.NewLogger(t), clock.System)
			_, err := r.onAccountVerifyEmail(context.Background(), &tg.AccountVerifyEmailRequest{
				Purpose:      &tg.EmailVerifyPurposeLoginSetup{PhoneNumber: "8618800000020", PhoneCodeHash: "hash-email-setup"},
				Verification: &tg.EmailVerificationCode{Code: "654321"},
			})
			assertRPCError(t, "account.verifyEmail", err, 400, rejection.typ)
			if account.verifyCalls != 0 {
				t.Fatalf("VerifyLoginEmail calls=%d, want 0 (refused before the email is set)", account.verifyCalls)
			}
			if authSvc.signInWithEmailCount != 0 {
				t.Fatalf("SignInWithEmail calls=%d, want 0", authSvc.signInWithEmailCount)
			}
		})
	}
}

// recordingRecoveryAccount 记录密码找回是否真的执行（找回会消耗找回码并重置 2FA 密码）。
type recordingRecoveryAccount struct {
	AccountService
	recoverCalls int
}

func (a *recordingRecoveryAccount) RecoverPassword(context.Context, int64, string, *domain.PasswordInputSettings) error {
	a.recoverCalls++
	return nil
}

// 名额已满或缺 device_id 时 auth.recoverPassword 在动 2FA 密码之前就拒绝：找回码不被消耗、密码不变。
func TestDeviceBindingRejectionRefusesRecoverPasswordBeforeResettingIt(t *testing.T) {
	for _, rejection := range deviceBindingRejections {
		t.Run(rejection.name, func(t *testing.T) {
			authSvc := fullDeviceLimitAuthService{&captureAuthService{pendingPasswordUserID: 42, pendingPassword: true}, rejection.err}
			account := &recordingRecoveryAccount{}
			r := New(Config{}, Deps{Auth: authSvc, Account: account}, zaptest.NewLogger(t), clock.System)
			_, err := r.onAuthRecoverPassword(pendingPasswordContext(), &tg.AuthRecoverPasswordRequest{Code: "123456"})
			assertRPCError(t, "auth.recoverPassword", err, 400, rejection.typ)
			if account.recoverCalls != 0 {
				t.Fatalf("RecoverPassword calls=%d, want 0 (refused before the password changes)", account.recoverCalls)
			}
			if authSvc.completePasswordCount != 0 {
				t.Fatalf("CompletePasswordSignIn calls=%d, want 0", authSvc.completePasswordCount)
			}
		})
	}
}

// 名额已满或缺 device_id 时扫码确认在清掉新设备的更新状态之前就拒绝。
func TestDeviceBindingRejectionRefusesAcceptLoginTokenBeforeClearingState(t *testing.T) {
	for _, rejection := range deviceBindingRejections {
		t.Run(rejection.name, func(t *testing.T) {
			updates := &captureUpdates{}
			r := New(Config{}, Deps{Auth: fullDeviceLimitAuthService{&captureAuthService{}, rejection.err}, Updates: updates}, zaptest.NewLogger(t), clock.System)
			_, err := acceptLoginTokenFromOtherDevice(t, r, 1000000109)
			assertRPCError(t, "auth.acceptLoginToken", err, 400, rejection.typ)
			if updates.cleared {
				t.Fatalf("new device update state cleared for %x, want untouched", updates.clearedAuthKeyID)
			}
		})
	}
}

// 扫码确认的其它错误保持原来的映射：系统账号 → AUTH_KEY_UNREGISTERED，其余 → 内部错误。
func TestAcceptLoginTokenKeepsOtherErrorMappings(t *testing.T) {
	const userID = int64(1000000108)
	cases := []struct {
		name    string
		bindErr error
		code    int
		typ     string
	}{
		{"system user", fmt.Errorf("bind: %w", auth.ErrSystemUserLoginForbidden), 401, "AUTH_KEY_UNREGISTERED"},
		{"store failure", errors.New("database is down"), 500, "INTERNAL_SERVER_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(Config{}, Deps{Auth: newDeviceLimitAuthService(tc.bindErr)}, zaptest.NewLogger(t), clock.System)
			_, err := acceptLoginTokenFromOtherDevice(t, r, userID)
			assertRPCError(t, "auth.acceptLoginToken", err, tc.code, tc.typ)
		})
	}
}

// acceptLoginTokenFromOtherDevice 在一台新设备上导出登录 token，再由 userID 已登录的另一台设备扫码确认。
func acceptLoginTokenFromOtherDevice(t *testing.T, r *Router, userID int64) (*tg.Authorization, error) {
	t.Helper()
	newDeviceCtx := WithSessionID(WithAuthKeyID(WithRawAuthKeyID(context.Background(),
		[8]byte{0x07, 0x01}), [8]byte{0x07, 0x02}), 701)
	exported, err := r.onAuthExportLoginToken(newDeviceCtx, &tg.AuthExportLoginTokenRequest{APIID: 2040, APIHash: "hash"})
	if err != nil {
		t.Fatalf("export login token: %v", err)
	}
	token, ok := exported.(*tg.AuthLoginToken)
	if !ok {
		t.Fatalf("export type = %T, want *tg.AuthLoginToken", exported)
	}
	scannerCtx := WithUserID(WithSessionID(WithAuthKeyID(WithRawAuthKeyID(context.Background(),
		[8]byte{0x07, 0x03}), [8]byte{0x07, 0x04}), 702), userID)
	return r.onAuthAcceptLoginToken(scannerCtx, token.Token)
}
