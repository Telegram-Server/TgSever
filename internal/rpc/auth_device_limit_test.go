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
