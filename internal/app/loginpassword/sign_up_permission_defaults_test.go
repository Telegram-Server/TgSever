package loginpassword_test

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 密码注册套用默认权限模板（#163）：真实的 auth.Service 读模板并把快照交给建号步骤，
// 登录密码与权限快照随账号一起写入。开关关闭或没有模板时不写权限。
func TestSignUpWithPasswordAppliesPermissionDefaults(t *testing.T) {
	cases := []struct {
		name      string
		template  bool
		apply     bool
		wantApply bool
	}{
		{name: "template+apply", template: true, apply: true, wantApply: true},
		{name: "template+noapply", template: true, apply: false},
		{name: "notemplate"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			users := memory.NewUserStore()
			passwords := memory.NewLoginPasswordStore(users)
			permissions := memory.NewAccountPermissionsStore(users)
			accounts := memory.NewAccountCreationStore(users, passwords, permissions)
			flows := auth.NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
				auth.WithSelfSignUpPermissionDefaults(permissions))
			now := time.Unix(1_800_000_000, 0)
			svc := loginpassword.NewService(users, passwords, accounts, memory.NewLoginPasswordChallengeStore(func() time.Time { return now }), flows,
				loginpassword.WithEnabled(true))

			member, err := users.Create(ctx, domain.User{Phone: "15550163700", FirstName: "Member"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.template {
				if _, _, _, err := permissions.UpdatePermissionDefaults(ctx, func(domain.PermissionDefaults, bool) domain.PermissionDefaults {
					d := domain.DefaultPermissionDefaults()
					d.Enabled = true
					d.Flags.Send = false
					d.SendWhitelistEnabled = true
					d.SendWhitelist = []int64{member.ID}
					d.ApplyToSelfSignup = tc.apply
					return d
				}); err != nil {
					t.Fatalf("write permission defaults: %v", err)
				}
			}

			phone := "1555016371" + string(rune('0'+i))
			u, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: [8]byte{0x63, byte(i)}}, "+"+phone, "Pass", "Word", newSettings(t, "secret"))
			if err != nil || u.ID == 0 {
				t.Fatalf("SignUp user=%d err=%v", u.ID, err)
			}
			if _, found, err := passwords.GetLoginPassword(ctx, u.ID); err != nil || !found {
				t.Fatalf("login password found=%v err=%v, want written with the account", found, err)
			}
			got, found, err := permissions.GetAccountPermissions(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if found != tc.wantApply {
				t.Fatalf("permissions found=%v (%+v), want %v", found, got, tc.wantApply)
			}
			if tc.wantApply && (!got.Enabled || got.Flags.Send || !got.SendWhitelistEnabled ||
				len(got.SendWhitelist) != 1 || got.SendWhitelist[0] != member.ID) {
				t.Fatalf("permissions = %+v, want the template snapshot (enabled, no send, whitelist [%d])", got, member.ID)
			}
		})
	}
}
