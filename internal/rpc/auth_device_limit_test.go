package rpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// deviceLimitAuthService 让验证码登录返回 domain.ErrDeviceLimitReached（包一层，模拟业务层
// 带上下文的 wrap）。
type deviceLimitAuthService struct{ *captureAuthService }

var errWrappedDeviceLimitReached = fmt.Errorf("upsert authorization: %w", domain.ErrDeviceLimitReached)

func (deviceLimitAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, errWrappedDeviceLimitReached
}

func TestDeviceLimitReachedMapsToAuthorizationsTooMuch(t *testing.T) {
	r := New(Config{}, Deps{Auth: deviceLimitAuthService{&captureAuthService{}}}, zaptest.NewLogger(t), clock.System)
	_, err := r.onAuthSignIn(context.Background(), &tg.AuthSignInRequest{PhoneNumber: "15550105201", PhoneCodeHash: "h", PhoneCode: "12345"})
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Code != 400 || rpcErr.Type != "AUTHORIZATIONS_TOO_MUCH" {
		t.Fatalf("auth.signIn err=%v, want 400 AUTHORIZATIONS_TOO_MUCH", err)
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
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Code != 400 || rpcErr.Type != "AUTHORIZATIONS_TOO_MUCH" {
		t.Fatalf("auth.checkPassword err=%v, want 400 AUTHORIZATIONS_TOO_MUCH", err)
	}
}

func TestDeviceLimitReachedOnRecoverPasswordMapsToAuthorizationsTooMuch(t *testing.T) {
	r := newTwoFactorDeviceLimitRouter(t)
	_, err := r.onAuthRecoverPassword(pendingPasswordContext(), &tg.AuthRecoverPasswordRequest{Code: "123456"})
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Code != 400 || rpcErr.Type != "AUTHORIZATIONS_TOO_MUCH" {
		t.Fatalf("auth.recoverPassword err=%v, want 400 AUTHORIZATIONS_TOO_MUCH", err)
	}
}
