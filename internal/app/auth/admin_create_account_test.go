package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type adminCreateAccountFixture struct {
	svc   *Service
	users *memory.UserStore
	auths *memory.AuthorizationStore
	sw    *signUpSwitch
}

func newAdminCreateAccountFixture(opts ...Option) adminCreateAccountFixture {
	f := adminCreateAccountFixture{
		users: memory.NewUserStore(),
		auths: memory.NewAuthorizationStore(),
		sw:    &signUpSwitch{},
	}
	messages := memory.NewMessageStore(memory.NewDialogStore())
	opts = append([]Option{
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
		WithSignUpAllowed(f.sw.allowed),
	}, opts...)
	f.svc = NewService(f.users, f.auths, memory.NewCodeStore(), nil, nil, "12345", opts...)
	return f
}

// 注册关闭时后台建号照常成功：账号按注册规则规范化，没有任何授权，之后可以用验证码登录。
func TestAdminCreateAccountIgnoresSignUpSwitchAndBindsNoAuthorization(t *testing.T) {
	ctx := context.Background()
	f := newAdminCreateAccountFixture()
	f.sw.set(false)

	u, err := f.svc.CreateAccount(ctx, "+1 555 000 7801", "  Alice ", " Admin ")
	if err != nil {
		t.Fatalf("CreateAccount with sign-up closed: %v", err)
	}
	if u.ID == 0 || u.Phone != "15550007801" || u.FirstName != "Alice" || u.LastName != "Admin" || u.AccessHash == 0 {
		t.Fatalf("created user = %+v, want normalized phone/name with id and access hash", u)
	}
	auths, err := f.auths.ListByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("authorizations after admin create = %d, want none", len(auths))
	}

	hash, err := f.svc.SendCode(ctx, "15550007801")
	if err != nil {
		t.Fatalf("SendCode for admin-created account with sign-up closed: %v", err)
	}
	var key [8]byte
	key[0] = 0x78
	signedIn, _, needSignUp, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, "15550007801", hash, "12345")
	if err != nil || needSignUp || signedIn.ID != u.ID {
		t.Fatalf("SignIn user=%d needSignUp=%v err=%v, want %d", signedIn.ID, needSignUp, err, u.ID)
	}
}

// 号码、名字校验与普通注册一致；号码已被占用时返回 domain.ErrPhoneNumberOccupied。
func TestAdminCreateAccountValidatesLikeSignUp(t *testing.T) {
	ctx := context.Background()
	f := newAdminCreateAccountFixture()
	if _, err := f.users.Create(ctx, domain.User{Phone: "15550007802", FirstName: "Existing"}); err != nil {
		t.Fatalf("seed existing user: %v", err)
	}
	longName := strings.Repeat("名", 65)
	cases := []struct {
		name             string
		phone, first, ln string
		want             error
	}{
		{"invalid phone format", "not-a-phone", "Bob", "", ErrPhoneNumberInvalid},
		{"empty phone", "", "Bob", "", ErrPhoneNumberInvalid},
		{"reserved system number", domain.OfficialSystemPhone, "Bob", "", ErrSystemUserLoginForbidden},
		{"empty first name", "15550007803", "   ", "", domain.ErrFirstNameInvalid},
		{"first name too long", "15550007803", longName, "", domain.ErrFirstNameInvalid},
		{"last name too long", "15550007803", "Bob", longName, domain.ErrFirstNameInvalid},
		{"phone occupied", "+15550007802", "Bob", "", domain.ErrPhoneNumberOccupied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.CreateAccount(ctx, tc.phone, tc.first, tc.ln); !errors.Is(err, tc.want) {
				t.Fatalf("CreateAccount err = %v, want %v", err, tc.want)
			}
			if _, err := f.svc.ValidateNewAccount(ctx, tc.phone, tc.first, tc.ln); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateNewAccount err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, found, _ := f.users.ByPhone(ctx, "15550007803"); found {
		t.Fatal("rejected CreateAccount must not create an account")
	}
}

// ValidateNewAccount 是试运行：返回规范化后的值，不建号。
func TestAdminValidateNewAccountNormalizesWithoutCreating(t *testing.T) {
	ctx := context.Background()
	f := newAdminCreateAccountFixture()
	f.sw.set(false)

	got, err := f.svc.ValidateNewAccount(ctx, "+1 555 000 7804", " Carol ", "")
	if err != nil {
		t.Fatalf("ValidateNewAccount: %v", err)
	}
	if got.ID != 0 || got.Phone != "15550007804" || got.FirstName != "Carol" || got.LastName != "" {
		t.Fatalf("ValidateNewAccount = %+v", got)
	}
	if _, found, _ := f.users.ByPhone(ctx, "15550007804"); found {
		t.Fatal("ValidateNewAccount must not create an account")
	}
}

// 会员赠送与普通注册一致。
func TestAdminCreateAccountPremiumGrantFollowsSignUp(t *testing.T) {
	ctx := context.Background()

	granted, err := newAdminCreateAccountFixture(WithPremiumGrant(3)).svc.CreateAccount(ctx, "15550007805", "Prem", "")
	if err != nil {
		t.Fatalf("CreateAccount with grant: %v", err)
	}
	wantMin := time.Now().AddDate(0, 3, 0).Add(-time.Minute).Unix()
	wantMax := time.Now().AddDate(0, 3, 0).Add(time.Minute).Unix()
	if int64(granted.PremiumUntil) < wantMin || int64(granted.PremiumUntil) > wantMax {
		t.Fatalf("PremiumUntil = %d, want ~now+3mo [%d,%d]", granted.PremiumUntil, wantMin, wantMax)
	}

	plain, err := newAdminCreateAccountFixture(WithPremiumGrant(0)).svc.CreateAccount(ctx, "15550007806", "Free", "")
	if err != nil {
		t.Fatalf("CreateAccount without grant: %v", err)
	}
	if plain.PremiumUntil != 0 {
		t.Fatalf("PremiumUntil = %d, want 0 with grant disabled", plain.PremiumUntil)
	}
}
