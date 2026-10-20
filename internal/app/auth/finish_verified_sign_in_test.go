package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// FinishVerifiedSignIn 是密码登录复用的收尾；它自己也要拒绝不能用手机号登录的账号，
// 不依赖调用方先查过。授权是否写入以 UserID 的结果判断。
//
// 没有 2FA 时的成功路径和 password_pending 由密码登录的端到端测试覆盖。
func TestFinishVerifiedSignInRefusesAccountsThatCannotSignInByPhone(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	keys := memory.NewAuthKeyStore()
	svc := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, nil, "12345")

	official, ok := domain.SystemUserByID(domain.OfficialSystemUserID)
	if !ok {
		t.Fatal("no official system user")
	}
	bot, err := users.Create(ctx, domain.User{FirstName: "Bot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	human, err := users.Create(ctx, domain.User{Phone: "15550330001", FirstName: "Gone"})
	if err != nil {
		t.Fatal(err)
	}
	deleted := human
	deleted.Deleted = true

	cases := map[string]domain.User{"system": official, "bot": bot, "deleted": deleted}
	var b byte = 0x21
	for name, u := range cases {
		b++
		key := testAuthKey(b)
		saveAuthKey(t, keys, key)
		if _, err := svc.FinishVerifiedSignIn(ctx, domain.Authorization{AuthKeyID: key.ID}, u); !errors.Is(err, ErrSystemUserLoginForbidden) {
			t.Fatalf("%s: FinishVerifiedSignIn = %v, want ErrSystemUserLoginForbidden", name, err)
		}
		if userID, authorized, err := svc.UserID(ctx, key.ID); err != nil || authorized || userID != 0 {
			t.Fatalf("%s: auth key bound to user %d (authorized=%v, err %v), want unbound", name, userID, authorized, err)
		}
	}
}
