package rpc

import (
	"context"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// rivalBeforeCreateUserStore 在第一次 Create 真正执行前，先用同一个号码建好另一个账号，
// 模拟并发注册：本请求已通过"号码无主"的检查，但另一个请求抢先建号成功。
type rivalBeforeCreateUserStore struct {
	store.UserStore
	once sync.Once
	err  error
}

func (s *rivalBeforeCreateUserStore) Create(ctx context.Context, u domain.User) (domain.User, error) {
	s.once.Do(func() {
		_, s.err = s.UserStore.Create(ctx, domain.User{AccessHash: 9, Phone: u.Phone, FirstName: "Rival"})
	})
	return s.UserStore.Create(ctx, u)
}

func TestAuthSignUpLosingPhoneRaceReturnsPhoneNumberOccupied(t *testing.T) {
	ctx := context.Background()
	users := &rivalBeforeCreateUserStore{UserStore: memory.NewUserStore()}
	svc := auth.NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	const phone = "15550013901"
	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345"); err != nil || !needSignUp {
		t.Fatalf("SignIn needSignUp=%v err=%v, want true/nil", needSignUp, err)
	}
	r := New(Config{}, Deps{Auth: svc}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithSessionID(WithAuthKeyID(ctx, [8]byte{3, 9, 0, 1}), 91)

	_, err = r.onAuthSignUp(reqCtx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: hash, FirstName: "Late"})
	if users.err != nil {
		t.Fatalf("create rival owner: %v", users.err)
	}
	assertPhoneRPCErr(t, err, "PHONE_NUMBER_OCCUPIED")
}
