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

// codeLoginDisabledAuthService 让每个验证码登录入口都返回 domain.ErrCodeLoginDisabled
// （包一层，模拟业务层带上下文的 wrap）。
type codeLoginDisabledAuthService struct{ *captureAuthService }

var errWrappedCodeLoginDisabled = fmt.Errorf("auth: %w", domain.ErrCodeLoginDisabled)

func (codeLoginDisabledAuthService) SendCode(context.Context, string) (string, error) {
	return "", errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAuthService) ResendCode(context.Context, string, string) (string, error) {
	return "", errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAuthService) SignInWithEmail(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAuthService) SignUp(context.Context, domain.Authorization, string, string, string, string) (domain.User, domain.Message, error) {
	return domain.User{}, domain.Message{}, errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAuthService) ConsumeLoginEmailReset(context.Context, string, string) (int64, error) {
	return 0, errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAuthService) SendPhoneCodeAfterLoginEmailReset(context.Context, string, int64) (string, error) {
	return "", errWrappedCodeLoginDisabled
}

// codeLoginDisabledAccount 让登录邮箱 setup 的两个入口返回 domain.ErrCodeLoginDisabled。
type codeLoginDisabledAccount struct{ AccountService }

func (codeLoginDisabledAccount) SendLoginEmailCode(context.Context, int64, string, string, string, bool) (string, int, error) {
	return "", 0, errWrappedCodeLoginDisabled
}
func (codeLoginDisabledAccount) VerifyLoginEmail(context.Context, int64, string, string, string, bool) (string, error) {
	return "", errWrappedCodeLoginDisabled
}

func TestCodeLoginDisabledMapsToCodeLoginDisabledRPCError(t *testing.T) {
	r := New(Config{}, Deps{
		Auth:    codeLoginDisabledAuthService{&captureAuthService{}},
		Account: codeLoginDisabledAccount{},
	}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()
	const phone = "15550009120"

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
		{"auth.resetLoginEmail", func() error {
			_, err := r.onAuthResetLoginEmail(ctx, &tg.AuthResetLoginEmailRequest{PhoneNumber: phone, PhoneCodeHash: "h"})
			return err
		}},
		{"account.sendVerifyEmailCode loginSetup", func() error {
			_, err := r.onAccountSendVerifyEmailCode(ctx, &tg.AccountSendVerifyEmailCodeRequest{
				Purpose: &tg.EmailVerifyPurposeLoginSetup{PhoneNumber: phone, PhoneCodeHash: "h"},
				Email:   "setup@example.test",
			})
			return err
		}},
		{"account.verifyEmail loginSetup", func() error {
			_, err := r.onAccountVerifyEmail(ctx, &tg.AccountVerifyEmailRequest{
				Purpose:      &tg.EmailVerifyPurposeLoginSetup{PhoneNumber: phone, PhoneCodeHash: "h"},
				Verification: &tg.EmailVerificationCode{Code: "123456"},
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			err := tc.call()
			rpcErr, ok := tgerr.As(err)
			if !ok || rpcErr.Code != 400 || rpcErr.Type != "CODE_LOGIN_DISABLED" {
				t.Fatalf("%s err=%v, want 400 CODE_LOGIN_DISABLED", tc.method, err)
			}
		})
	}
}
