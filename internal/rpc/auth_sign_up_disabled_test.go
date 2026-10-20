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

// signUpDisabledAuthService 让每个会为新号码建号的入口都返回 domain.ErrSignUpDisabled
// （包一层，模拟业务层带上下文的 wrap）。
type signUpDisabledAuthService struct{ *captureAuthService }

var errWrappedSignUpDisabled = fmt.Errorf("auth: %w", domain.ErrSignUpDisabled)

func (signUpDisabledAuthService) SendCode(context.Context, string) (string, error) {
	return "", errWrappedSignUpDisabled
}
func (signUpDisabledAuthService) ResendCode(context.Context, string, string) (string, error) {
	return "", errWrappedSignUpDisabled
}
func (signUpDisabledAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, errWrappedSignUpDisabled
}
func (signUpDisabledAuthService) SignInWithEmail(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, errWrappedSignUpDisabled
}
func (signUpDisabledAuthService) SignUp(context.Context, domain.Authorization, string, string, string, string) (domain.User, domain.Message, error) {
	return domain.User{}, domain.Message{}, errWrappedSignUpDisabled
}

// signUpDisabledLoginPassword 让密码登录里会为新号码建号的两个入口返回 domain.ErrSignUpDisabled。
type signUpDisabledLoginPassword struct{ LoginPasswordService }

func (signUpDisabledLoginPassword) Enabled() bool { return true }
func (signUpDisabledLoginPassword) GetLoginPassword(context.Context, [8]byte, string) (domain.PasswordSettings, error) {
	return domain.PasswordSettings{}, errWrappedSignUpDisabled
}
func (signUpDisabledLoginPassword) SignUp(context.Context, domain.Authorization, string, string, string, domain.PasswordInputSettings) (domain.User, error) {
	return domain.User{}, errWrappedSignUpDisabled
}

func TestSignUpDisabledMapsToSignUpDisabledRPCError(t *testing.T) {
	r := New(Config{}, Deps{
		Auth:          signUpDisabledAuthService{&captureAuthService{}},
		LoginPassword: signUpDisabledLoginPassword{},
	}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()
	const phone = "15550007520"

	cases := []struct {
		method string
		call   func() error
	}{
		{"auth.sendCode", func() error {
			_, err := r.onAuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone})
			return err
		}},
		{"auth.resendCode", func() error {
			_, err := r.onAuthResendCode(ctx, &tg.AuthResendCodeRequest{PhoneNumber: phone, PhoneCodeHash: "h"})
			return err
		}},
		{"auth.signIn", func() error {
			_, err := r.onAuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: "h", PhoneCode: "12345"})
			return err
		}},
		{"auth.signIn email_verification", func() error {
			req := &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: "h"}
			req.SetEmailVerification(&tg.EmailVerificationCode{Code: "123456"})
			_, err := r.onAuthSignIn(ctx, req)
			return err
		}},
		{"auth.signUp", func() error {
			_, err := r.onAuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: "h", FirstName: "New"})
			return err
		}},
		{"telesrv.getLoginPassword", func() error {
			_, err := r.onTelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			return err
		}},
		{"telesrv.signUpWithPassword", func() error {
			_, err := r.onTelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{PhoneNumber: phone, FirstName: "New"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			err := tc.call()
			rpcErr, ok := tgerr.As(err)
			if !ok || rpcErr.Code != 400 || rpcErr.Type != "SIGN_UP_DISABLED" {
				t.Fatalf("%s err=%v, want 400 SIGN_UP_DISABLED", tc.method, err)
			}
		})
	}
}
