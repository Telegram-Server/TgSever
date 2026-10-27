package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 密码注册没有验证码可作废：auth key 不能绑定授权时必须在建号之前失败，
// 否则号码会被一个没人登录的新账号永久占住。
func TestSignUpWithAccountCreatorRejectsUnbindableAuthKeyBeforeCreating(t *testing.T) {
	authKeys := memory.NewAuthKeyStore()
	users := memory.NewUserStore()
	s := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), authKeys, memory.NewTempAuthKeyBindingStore(authKeys), "12345")

	created := false
	_, err := s.SignUpWithAccountCreator(context.Background(), domain.Authorization{AuthKeyID: [8]byte{9, 9, 9}}, "15550310900", "Nobody", "",
		func(ctx context.Context, u domain.User, _ *domain.AccountPermissions) (domain.User, error) {
			created = true
			return users.Create(ctx, u)
		})
	if !errors.Is(err, ErrAuthKeyPermEmpty) {
		t.Fatalf("sign up over an unknown auth key err=%v, want ErrAuthKeyPermEmpty", err)
	}
	if created {
		t.Fatal("account creator ran although the auth key cannot be bound")
	}
	if _, found, _ := users.ByPhone(context.Background(), "15550310900"); found {
		t.Fatal("phone is occupied after a sign-up that could not bind")
	}
}
