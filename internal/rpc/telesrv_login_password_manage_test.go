package rpc

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	tdsrp "github.com/iamxvbaba/td/crypto/srp"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store/memory"
)

// failingAuthorizationLookup 的 Authorization 一律失败，并记下被调用的次数。
type failingAuthorizationLookup struct {
	*captureAuthService
	lookups int
}

func (s *failingAuthorizationLookup) Authorization(context.Context, [8]byte) (domain.Authorization, bool, error) {
	s.lookups++
	return domain.Authorization{}, false, errors.New("authorization store down")
}

// unusedAuthFlows 满足 loginpassword.AuthFlows；已登录的管理方法不会走到登录/注册收尾。
type unusedAuthFlows struct{}

func (unusedAuthFlows) SignUpWithAccountCreator(context.Context, domain.Authorization, string, string, string, auth.SignUpAccountFunc) (domain.User, error) {
	return domain.User{}, errors.New("unexpected sign-up")
}

func (unusedAuthFlows) CheckSignUpAllowed(context.Context) error { return nil }

func (unusedAuthFlows) FinishVerifiedSignIn(context.Context, domain.Authorization, domain.User) (domain.User, error) {
	return domain.User{}, errors.New("unexpected sign-in")
}

func loginPasswordUpdateRequest(t *testing.T, password string) *tg.TelesrvUpdateLoginPasswordRequest {
	t.Helper()
	algo := srp.DefaultAlgo()
	hash, salt1, err := tdsrp.NewSRP(rand.Reader).NewHash([]byte(password), tdsrp.Input{Salt1: algo.Salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P})
	if err != nil {
		t.Fatalf("td NewHash: %v", err)
	}
	settings := tg.AccountPasswordInputSettings{}
	settings.SetNewAlgo(&tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{Salt1: salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P})
	settings.SetNewPasswordHash(hash)
	return &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: settings}
}

// 首次设置登录密码（账号还没有）不读当前授权：即使授权读取会失败，设置也成功；只有已有
// 登录密码而 current 为空（忘记密码重设）时才读，读取失败按内部错误返回。
func TestTelesrvUpdateLoginPasswordReadsTheAuthorizationOnlyForAReset(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	u, err := users.Create(ctx, domain.User{AccessHash: 7, Phone: "15550341020", FirstName: "Lazy"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	authSvc := &failingAuthorizationLookup{captureAuthService: &captureAuthService{}}
	svc := loginpassword.NewService(users, memory.NewLoginPasswordStore(users), nil, memory.NewLoginPasswordChallengeStore(time.Now), unusedAuthFlows{},
		loginpassword.WithEnabled(true), loginpassword.WithResetWindow(10*time.Minute))
	router := New(Config{}, Deps{Auth: authSvc, LoginPassword: svc}, zaptest.NewLogger(t), clock.System)
	authKey := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	callCtx := WithRawAuthKeyID(WithAuthKeyID(WithUserID(ctx, u.ID), authKey), authKey)

	ok, err := router.onTelesrvUpdateLoginPassword(callCtx, loginPasswordUpdateRequest(t, "first"))
	if err != nil || !ok {
		t.Fatalf("first-time set with a failing authorization lookup = ok %v err %v, want true/nil", ok, err)
	}
	if authSvc.lookups != 0 {
		t.Fatalf("first-time set read the authorization %d times, want 0", authSvc.lookups)
	}

	if _, err := router.onTelesrvUpdateLoginPassword(callCtx, loginPasswordUpdateRequest(t, "second")); !tgerr.Is(err, "INTERNAL_SERVER_ERROR") {
		t.Fatalf("reset with a failing authorization lookup = %v, want INTERNAL_SERVER_ERROR", err)
	}
	if authSvc.lookups != 1 {
		t.Fatalf("reset read the authorization %d times, want 1", authSvc.lookups)
	}
}
