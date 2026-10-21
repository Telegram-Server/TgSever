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

var errWrappedDeviceLimitReached = fmt.Errorf("upsert authorization: %w", domain.ErrDeviceLimitReached)

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
type deviceLimitLoginPassword struct{ LoginPasswordService }

func (deviceLimitLoginPassword) Enabled() bool { return true }
func (deviceLimitLoginPassword) SignIn(context.Context, domain.Authorization, [8]byte, string, domain.PasswordCheck) (domain.User, error) {
	return domain.User{}, errWrappedDeviceLimitReached
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

func TestDeviceLimitReachedMapsToAuthorizationsTooMuch(t *testing.T) {
	const userID = int64(1000000107)
	r := New(Config{}, Deps{
		Auth:          newDeviceLimitAuthService(errWrappedDeviceLimitReached),
		LoginPassword: deviceLimitLoginPassword{},
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
		assertRPCError(t, tc.method, tc.call(), 400, "AUTHORIZATIONS_TOO_MUCH")
	}
}

// twoFactorDeviceLimitAuthService 让 2FA 转正返回 domain.ErrDeviceLimitReached（#106）。
type twoFactorDeviceLimitAuthService struct{ *captureAuthService }

func (twoFactorDeviceLimitAuthService) CompletePasswordSignIn(context.Context, [8]byte, int64) error {
	return errWrappedDeviceLimitReached
}

// acceptingPasswordAccountService 让任何 2FA 密码校验与密码找回都通过。
type acceptingPasswordAccountService struct{ AccountService }

func (acceptingPasswordAccountService) CheckPassword(context.Context, int64, domain.PasswordCheck) error {
	return nil
}

func (acceptingPasswordAccountService) RecoverPassword(context.Context, int64, string, *domain.PasswordInputSettings) error {
	return nil
}

func newTwoFactorDeviceLimitRouter(t *testing.T) *Router {
	auth := twoFactorDeviceLimitAuthService{&captureAuthService{pendingPasswordUserID: 42, pendingPassword: true}}
	return New(Config{}, Deps{Auth: auth, Account: acceptingPasswordAccountService{}}, zaptest.NewLogger(t), clock.System)
}

func TestDeviceLimitReachedOnCheckPasswordMapsToAuthorizationsTooMuch(t *testing.T) {
	r := newTwoFactorDeviceLimitRouter(t)
	_, err := r.onAuthCheckPassword(pendingPasswordContext(), &tg.InputCheckPasswordSRP{SRPID: 1, A: []byte{1}, M1: []byte{1}})
	assertRPCError(t, "auth.checkPassword", err, 400, "AUTHORIZATIONS_TOO_MUCH")
}

func TestDeviceLimitReachedOnRecoverPasswordMapsToAuthorizationsTooMuch(t *testing.T) {
	r := newTwoFactorDeviceLimitRouter(t)
	_, err := r.onAuthRecoverPassword(pendingPasswordContext(), &tg.AuthRecoverPasswordRequest{Code: "123456"})
	assertRPCError(t, "auth.recoverPassword", err, 400, "AUTHORIZATIONS_TOO_MUCH")
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
