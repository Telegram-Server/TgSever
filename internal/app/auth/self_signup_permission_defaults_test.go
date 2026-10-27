package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// 自助注册套用默认权限模板（#163）：模板存在且"自助注册也套用"开启时，验证码注册与密码注册
// 建出的账号从诞生起就带模板的权限快照；开关关闭或没有模板时不写权限；读取模板失败时注册失败
// 且不建号。

type selfSignUpDefaultsFixture struct {
	signUpSwitchFixture
	permissions *memory.AccountPermissionsStore
	accounts    *memory.AccountCreationStore
}

func newSelfSignUpDefaultsFixture(t *testing.T, defaults PermissionDefaultsReader) selfSignUpDefaultsFixture {
	t.Helper()
	users := memory.NewUserStore()
	permissions := memory.NewAccountPermissionsStore(users)
	accounts := memory.NewAccountCreationStore(users, memory.NewLoginPasswordStore(users), permissions)
	if defaults == nil {
		defaults = permissions
	}
	f := newSignUpSwitchFixtureWithUsers(t, users,
		WithAccountCreation(accounts),
		WithSelfSignUpPermissionDefaults(defaults))
	return selfSignUpDefaultsFixture{signUpSwitchFixture: f, permissions: permissions, accounts: accounts}
}

// newSignUpSwitchFixtureWithUsers 与 newSignUpSwitchFixture 相同，只是使用给定的 users store
// （建号 store 与权限 store 要和它共享同一份账号）。
func newSignUpSwitchFixtureWithUsers(t *testing.T, users *memory.UserStore, opts ...Option) signUpSwitchFixture {
	t.Helper()
	f := signUpSwitchFixture{users: users, codes: memory.NewCodeStore(), sender: &captureOTPSender{}, sw: &signUpSwitch{}}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	opts = append([]Option{
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
		WithPasswords(memory.NewPasswordStore()),
		WithPhoneCodeDelivery(f.sender, 5),
		WithSignUpAllowed(f.sw.allowed),
	}, opts...)
	f.svc = NewService(f.users, memory.NewAuthorizationStore(), f.codes, nil, nil, "12345", opts...)
	return f
}

// setDefaults 写入一份模板：总开关开、禁止发起私聊、白名单开且只含 member。
func (f selfSignUpDefaultsFixture) setDefaults(t *testing.T, applyToSelfSignUp bool, member int64) domain.PermissionDefaults {
	t.Helper()
	_, _, written, err := f.permissions.UpdatePermissionDefaults(context.Background(), func(domain.PermissionDefaults, bool) domain.PermissionDefaults {
		d := domain.DefaultPermissionDefaults()
		d.Enabled = true
		d.Flags.CreateGroup = false
		d.SendWhitelistEnabled = true
		d.SendWhitelist = []int64{member}
		d.ApplyToSelfSignup = applyToSelfSignUp
		d.CommandID = "cmd-template"
		return d
	})
	if err != nil {
		t.Fatalf("write permission defaults: %v", err)
	}
	return written
}

// codeSignUp 走一遍验证码注册：SendCode → SignIn（需注册）→ SignUp。
func (f selfSignUpDefaultsFixture) codeSignUp(ctx context.Context, t *testing.T, phone string, key byte) (domain.User, error) {
	t.Helper()
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, f.svc, phone, hash, f.lastCode(t))
	u, _, err := f.svc.SignUp(ctx, domain.Authorization{AuthKeyID: [8]byte{key}}, phone, hash, "Self", "Signup")
	return u, err
}

// passwordSignUp 走密码注册的后半段：建号交给原子建号操作，同时写登录密码与权限快照。
func (f selfSignUpDefaultsFixture) passwordSignUp(ctx context.Context, phone string, key byte) (domain.User, error) {
	return f.svc.SignUpWithAccountCreator(ctx, domain.Authorization{AuthKeyID: [8]byte{key}}, phone, "Self", "Password",
		func(ctx context.Context, u domain.User, permissions *domain.AccountPermissions) (domain.User, error) {
			return f.accounts.CreateAccount(ctx, u, store.AccountExtras{Permissions: permissions})
		})
}

func TestSelfSignUpAppliesPermissionDefaultsMatrix(t *testing.T) {
	type signUpPath func(f selfSignUpDefaultsFixture, ctx context.Context, t *testing.T, phone string, key byte) (domain.User, error)
	paths := map[string]signUpPath{
		"code": func(f selfSignUpDefaultsFixture, ctx context.Context, t *testing.T, phone string, key byte) (domain.User, error) {
			return f.codeSignUp(ctx, t, phone, key)
		},
		"password": func(f selfSignUpDefaultsFixture, ctx context.Context, _ *testing.T, phone string, key byte) (domain.User, error) {
			return f.passwordSignUp(ctx, phone, key)
		},
	}
	cases := []struct {
		name      string
		template  bool
		apply     bool
		wantApply bool
	}{
		{name: "template+apply", template: true, apply: true, wantApply: true},
		{name: "template+noapply", template: true, apply: false},
		{name: "notemplate", template: false},
	}
	for pathName, signUp := range paths {
		for i, tc := range cases {
			t.Run(pathName+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				f := newSelfSignUpDefaultsFixture(t, nil)
				member := f.existingUser(t, "15550163900")
				if tc.template {
					f.setDefaults(t, tc.apply, member.ID)
				}
				u, err := signUp(f, ctx, t, "15550163"+string(rune('1'+i))+"01", byte(0x60+i))
				if err != nil || u.ID == 0 {
					t.Fatalf("sign up user=%d err=%v", u.ID, err)
				}
				got, found, err := f.permissions.GetAccountPermissions(ctx, u.ID)
				if err != nil {
					t.Fatalf("read permissions: %v", err)
				}
				if !tc.wantApply {
					if found {
						t.Fatalf("permissions written although the template does not apply: %+v", got)
					}
					return
				}
				if !found {
					t.Fatal("no permissions written for a self sign-up while the template applies")
				}
				if !got.Enabled || got.Flags.CreateGroup || !got.SendWhitelistEnabled ||
					len(got.SendWhitelist) != 1 || got.SendWhitelist[0] != member.ID || got.UserID != u.ID {
					t.Fatalf("permissions = %+v, want the template snapshot (enabled, no group creation, whitelist [%d])", got, member.ID)
				}
				if got.CommandID != "" {
					t.Fatalf("self sign-up snapshot command id = %q, want empty", got.CommandID)
				}
			})
		}
	}
}

// 每次注册都重新读模板（不缓存）：注册之间改了模板，下一个账号拿到的是新模板。
func TestSelfSignUpReadsPermissionDefaultsEveryTime(t *testing.T) {
	ctx := context.Background()
	f := newSelfSignUpDefaultsFixture(t, nil)
	member := f.existingUser(t, "15550163950")
	first, err := f.passwordSignUp(ctx, "15550163951", 0x70)
	if err != nil {
		t.Fatalf("first sign up: %v", err)
	}
	f.setDefaults(t, true, member.ID)
	second, err := f.passwordSignUp(ctx, "15550163952", 0x71)
	if err != nil {
		t.Fatalf("second sign up: %v", err)
	}
	if _, found, _ := f.permissions.GetAccountPermissions(ctx, first.ID); found {
		t.Fatal("account created before the template exists got permissions")
	}
	if got, found, _ := f.permissions.GetAccountPermissions(ctx, second.ID); !found || !got.Enabled {
		t.Fatalf("account created after the template was switched on: found=%v %+v", found, got)
	}
}

type failingPermissionDefaults struct{ err error }

func (f failingPermissionDefaults) GetPermissionDefaults(context.Context) (domain.PermissionDefaults, bool, error) {
	return domain.PermissionDefaults{}, false, f.err
}

// 读取模板失败时注册失败，且不建号：两条路径都不能留下一个不受控的账号。
func TestSelfSignUpPermissionDefaultsReadFailureCreatesNoAccount(t *testing.T) {
	boom := errors.New("defaults unavailable")
	ctx := context.Background()

	f := newSelfSignUpDefaultsFixture(t, failingPermissionDefaults{err: boom})
	if _, err := f.codeSignUp(ctx, t, "15550163981", 0x80); !errors.Is(err, boom) {
		t.Fatalf("code sign up err=%v, want %v", err, boom)
	}
	if _, found, _ := f.users.ByPhone(ctx, "15550163981"); found {
		t.Fatal("code sign up created an account although the template could not be read")
	}

	created := false
	_, err := f.svc.SignUpWithAccountCreator(ctx, domain.Authorization{AuthKeyID: [8]byte{0x81}}, "15550163982", "Self", "",
		func(ctx context.Context, u domain.User, permissions *domain.AccountPermissions) (domain.User, error) {
			created = true
			return f.accounts.CreateAccount(ctx, u, store.AccountExtras{Permissions: permissions})
		})
	if !errors.Is(err, boom) {
		t.Fatalf("password sign up err=%v, want %v", err, boom)
	}
	if created {
		t.Fatal("password sign up ran the account creator although the template could not be read")
	}
}
