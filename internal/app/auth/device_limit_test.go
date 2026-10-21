package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 设备上限（#105）：账号设了上限后，已完成登录的授权数（不算当前 auth key 自身、不算 2FA
// 未完成的）达到上限时，验证码登录新设备返回 domain.ErrDeviceLimitReached，且不写入授权。
// 没设上限的账号不受限制；bot 不受限制。

type deviceLimitFixture struct {
	signUpSwitchFixture
	auths     *memory.AuthorizationStore
	bots      *memory.BotStore
	passwords *memory.PasswordStore
}

func newDeviceLimitFixture(t *testing.T) deviceLimitFixture {
	t.Helper()
	f := deviceLimitFixture{signUpSwitchFixture: newSignUpSwitchFixture(t), auths: memory.NewAuthorizationStore(), passwords: memory.NewPasswordStore()}
	f.bots = memory.NewBotStore(f.users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	f.svc = NewService(f.users, f.auths, f.codes, nil, nil, "12345",
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
		WithPasswords(f.passwords),
		WithPhoneCodeDelivery(f.sender, 5),
		WithSignUpAllowed(f.sw.allowed),
		WithBotLogin(f.bots))
	return f
}

func deviceKey(n byte) [8]byte { return [8]byte{0xde, 0x71, n} }

// signInOn 在 key 这台设备上走一遍验证码登录。
func (f deviceLimitFixture) signInOn(t *testing.T, phone string, key [8]byte) error {
	t.Helper()
	ctx := context.Background()
	hash, err := f.svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	_, _, needSignUp, err := f.svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, f.lastCode(t))
	if err == nil && needSignUp {
		t.Fatalf("SignIn %s asked to sign up, want an existing account", phone)
	}
	return err
}

func (f deviceLimitFixture) setLimit(t *testing.T, userID int64, limit int) {
	t.Helper()
	if err := f.auths.SetDeviceLimit(context.Background(), userID, limit); err != nil {
		t.Fatalf("SetDeviceLimit(%d): %v", limit, err)
	}
}

func (f deviceLimitFixture) assertDevices(t *testing.T, userID int64, want int) {
	t.Helper()
	got, err := f.svc.ListAuthorizations(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListAuthorizations: %v", err)
	}
	if len(got) != want {
		t.Fatalf("authorizations=%d, want %d", len(got), want)
	}
}

func TestDeviceLimitUnsetDoesNotLimitCodeLogin(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105001"
	u := f.existingUser(t, phone)
	for i := byte(1); i <= 5; i++ {
		if err := f.signInOn(t, phone, deviceKey(i)); err != nil {
			t.Fatalf("sign in on device %d without a limit: %v", i, err)
		}
	}
	f.assertDevices(t, u.ID, 5)
}

func TestDeviceLimitRejectsCodeLoginOnNewDeviceAtLimit(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105002"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 2)
	for i := byte(1); i <= 2; i++ {
		if err := f.signInOn(t, phone, deviceKey(i)); err != nil {
			t.Fatalf("sign in on device %d below the limit: %v", i, err)
		}
	}
	err := f.signInOn(t, phone, deviceKey(3))
	if !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("sign in on a third device err=%v, want ErrDeviceLimitReached", err)
	}
	f.assertDevices(t, u.ID, 2)
	if _, found, _ := f.svc.Authorization(context.Background(), deviceKey(3)); found {
		t.Fatal("rejected device was bound")
	}
}

func TestDeviceLimitAllowsSignInAgainOnTheSameAuthKey(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105003"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	if err := f.signInOn(t, phone, deviceKey(1)); err != nil {
		t.Fatalf("first sign in: %v", err)
	}
	if err := f.signInOn(t, phone, deviceKey(1)); err != nil {
		t.Fatalf("sign in again on the same auth key: %v", err)
	}
	f.assertDevices(t, u.ID, 1)
}

func TestDeviceLimitFreesASlotAfterLogOut(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105004"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	if err := f.signInOn(t, phone, deviceKey(1)); err != nil {
		t.Fatalf("first sign in: %v", err)
	}
	if err := f.signInOn(t, phone, deviceKey(2)); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("second device before log out err=%v, want ErrDeviceLimitReached", err)
	}
	if err := f.svc.LogOut(context.Background(), deviceKey(1)); err != nil {
		t.Fatalf("LogOut: %v", err)
	}
	if err := f.signInOn(t, phone, deviceKey(2)); err != nil {
		t.Fatalf("second device after log out: %v", err)
	}
	f.assertDevices(t, u.ID, 1)
}

func TestDeviceLimitBelowCurrentDevicesKeepsThemAndRejectsNewLogins(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105005"
	u := f.existingUser(t, phone)
	for i := byte(1); i <= 3; i++ {
		if err := f.signInOn(t, phone, deviceKey(i)); err != nil {
			t.Fatalf("sign in on device %d: %v", i, err)
		}
	}
	f.setLimit(t, u.ID, 1)
	f.assertDevices(t, u.ID, 3)
	for i := byte(1); i <= 3; i++ {
		if id, found, err := f.svc.UserID(context.Background(), deviceKey(i)); err != nil || !found || id != u.ID {
			t.Fatalf("device %d after lowering the limit user=%d found=%v err=%v, want still signed in", i, id, found, err)
		}
	}
	if err := f.signInOn(t, phone, deviceKey(4)); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("new device over the lowered limit err=%v, want ErrDeviceLimitReached", err)
	}
	f.assertDevices(t, u.ID, 3)
}

func TestDeviceLimitDoesNotApplyToBots(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	bot, _, err := f.bots.CreateBotAccount(ctx, domain.User{FirstName: "Limited", Username: "limited_bot"},
		domain.BotProfile{OwnerUserID: 1, TokenSecret: "device-limit-secret"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	// 后台不允许给 bot 设上限；这里直接写 store，模拟一条遗留的上限也不能挡住 bot 登录。
	f.setLimit(t, bot.ID, 1)
	token := domain.FormatBotToken(bot.ID, "device-limit-secret")
	for i := byte(1); i <= 3; i++ {
		if _, err := f.svc.SignInBot(ctx, domain.Authorization{AuthKeyID: deviceKey(i)}, token); err != nil {
			t.Fatalf("bot sign in on key %d: %v", i, err)
		}
	}
	f.assertDevices(t, bot.ID, 3)
}

// 设备上限（#107）：登录密码（FinishVerifiedSignIn）、Passkey（BindVerifiedLogin）、扫码确认
// （AcceptLoginToken）与验证码登录走同一个绑定步骤，达到上限时都返回 domain.ErrDeviceLimitReached，
// 且新设备不会被绑定。
func TestDeviceLimitRejectsEveryOtherLoginEntryOnNewDeviceAtLimit(t *testing.T) {
	entries := []struct {
		name  string
		phone string
		login func(f deviceLimitFixture, u domain.User, key [8]byte) error
	}{
		{"login password", "15550107001", func(f deviceLimitFixture, u domain.User, key [8]byte) error {
			_, err := f.svc.FinishVerifiedSignIn(context.Background(), domain.Authorization{AuthKeyID: key}, u)
			return err
		}},
		{"passkey", "15550107002", func(f deviceLimitFixture, u domain.User, key [8]byte) error {
			_, err := f.svc.BindVerifiedLogin(context.Background(), domain.Authorization{AuthKeyID: key}, u.ID)
			return err
		}},
		{"qr accept", "15550107003", func(f deviceLimitFixture, u domain.User, key [8]byte) error {
			_, err := f.svc.AcceptLoginToken(context.Background(), domain.Authorization{AuthKeyID: key}, u.ID)
			return err
		}},
	}
	for _, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			f := newDeviceLimitFixture(t)
			u := f.existingUser(t, entry.phone)
			f.setLimit(t, u.ID, 1)
			if err := entry.login(f, u, deviceKey(1)); err != nil {
				t.Fatalf("first device below the limit: %v", err)
			}
			err := entry.login(f, u, deviceKey(2))
			if !errors.Is(err, domain.ErrDeviceLimitReached) {
				t.Fatalf("second device err=%v, want ErrDeviceLimitReached", err)
			}
			f.assertDevices(t, u.ID, 1)
			if _, found, _ := f.svc.Authorization(context.Background(), deviceKey(2)); found {
				t.Fatal("rejected device was bound")
			}
		})
	}
}
