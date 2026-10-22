package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// 设备上限按设备绑定执行（#105、#112）：设了上限的账号，一台设备（auth key 上持久化的
// device_id）第一次在该账号上完成登录即被绑定，名额 = 绑定数。名额满时新设备返回
// domain.ErrDeviceLimitReached，没上报 device_id 的设备返回 domain.ErrDeviceIDRequired，
// 都不写入授权；退出登录不解除绑定。没设上限的账号与 bot 不受限制。

type deviceLimitFixture struct {
	signUpSwitchFixture
	auths     *memory.AuthorizationStore
	keys      *memory.AuthKeyStore
	bots      *memory.BotStore
	passwords *memory.PasswordStore
}

func newDeviceLimitFixture(t *testing.T) deviceLimitFixture {
	t.Helper()
	f := deviceLimitFixture{
		signUpSwitchFixture: newSignUpSwitchFixture(t),
		auths:               memory.NewAuthorizationStore(),
		keys:                memory.NewAuthKeyStore(),
		passwords:           memory.NewPasswordStore(),
	}
	f.bots = memory.NewBotStore(f.users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	f.svc = NewService(f.users, f.auths, f.codes, f.keys, nil, "12345",
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
		WithPasswords(f.passwords),
		WithPhoneCodeDelivery(f.sender, 5),
		WithSignUpAllowed(f.sw.allowed),
		WithBotLogin(f.bots))
	return f
}

// key 建第 n 把 permanent auth key；deviceID 非空时像客户端在 initConnection.params 里
// 上报过那样持久化到 key 上。
func (f deviceLimitFixture) key(t *testing.T, n byte, deviceID string) [8]byte {
	t.Helper()
	ctx := context.Background()
	id := [8]byte{0xde, 0x71, n}
	if err := f.keys.Save(ctx, store.AuthKeyData{ID: id, ServerSalt: 1}); err != nil {
		t.Fatalf("save auth key %d: %v", n, err)
	}
	if deviceID != "" {
		if err := f.svc.UpdateAuthKeyClientInfo(ctx, id, domain.AuthKeyClientInfo{DeviceID: deviceID}); err != nil {
			t.Fatalf("persist device id on key %d: %v", n, err)
		}
	}
	return id
}

// device 是第 n 台设备上的第一把 auth key，device_id 为 "device-n"。
func (f deviceLimitFixture) device(t *testing.T, n byte) [8]byte {
	t.Helper()
	return f.key(t, n, fmt.Sprintf("device-%d", n))
}

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

func (f deviceLimitFixture) assertBindings(t *testing.T, userID int64, want int) {
	t.Helper()
	got, err := f.auths.ListDeviceBindings(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListDeviceBindings: %v", err)
	}
	if len(got) != want {
		t.Fatalf("device bindings=%+v, want %d", got, want)
	}
}

func (f deviceLimitFixture) assertNotSignedIn(t *testing.T, key [8]byte) {
	t.Helper()
	if _, found, _ := f.svc.Authorization(context.Background(), key); found {
		t.Fatalf("rejected key %x was bound", key)
	}
}

func TestDeviceLimitUnsetDoesNotLimitCodeLoginOrBindDevices(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105001"
	u := f.existingUser(t, phone)
	for i := byte(1); i <= 5; i++ {
		// 没有上限时，没上报 device_id 的客户端照常登录。
		if err := f.signInOn(t, phone, f.key(t, i, "")); err != nil {
			t.Fatalf("sign in on device %d without a limit: %v", i, err)
		}
	}
	f.assertDevices(t, u.ID, 5)
	f.assertBindings(t, u.ID, 0)
}

func TestDeviceLimitRejectsCodeLoginOnNewDeviceAtLimit(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105002"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 2)
	for i := byte(1); i <= 2; i++ {
		if err := f.signInOn(t, phone, f.device(t, i)); err != nil {
			t.Fatalf("sign in on device %d below the limit: %v", i, err)
		}
	}
	third := f.device(t, 3)
	if err := f.signInOn(t, phone, third); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("sign in on a third device err=%v, want ErrDeviceLimitReached", err)
	}
	f.assertDevices(t, u.ID, 2)
	f.assertBindings(t, u.ID, 2)
	f.assertNotSignedIn(t, third)
}

func TestDeviceLimitRejectsCodeLoginWithoutDeviceID(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105006"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 2)
	key := f.key(t, 1, "")
	if err := f.signInOn(t, phone, key); !errors.Is(err, domain.ErrDeviceIDRequired) {
		t.Fatalf("sign in without a device id err=%v, want ErrDeviceIDRequired", err)
	}
	f.assertDevices(t, u.ID, 0)
	f.assertBindings(t, u.ID, 0)
	f.assertNotSignedIn(t, key)
}

func TestDeviceLimitAllowsSignInAgainOnTheSameAuthKey(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105003"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	key := f.device(t, 1)
	if err := f.signInOn(t, phone, key); err != nil {
		t.Fatalf("first sign in: %v", err)
	}
	if err := f.signInOn(t, phone, key); err != nil {
		t.Fatalf("sign in again on the same auth key: %v", err)
	}
	f.assertDevices(t, u.ID, 1)
	f.assertBindings(t, u.ID, 1)
}

// 退出登录不解除绑定：名额仍被占着，新设备登不上；同一台设备（重装后换了 auth key）能再登录。
func TestDeviceLimitLogOutKeepsTheBinding(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550105004"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	first := f.device(t, 1)
	if err := f.signInOn(t, phone, first); err != nil {
		t.Fatalf("first sign in: %v", err)
	}
	if err := f.svc.LogOut(ctx, first); err != nil {
		t.Fatalf("LogOut: %v", err)
	}
	if err := f.signInOn(t, phone, f.device(t, 2)); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("new device after log out err=%v, want ErrDeviceLimitReached", err)
	}
	reinstalled := f.key(t, 3, "device-1")
	if err := f.signInOn(t, phone, reinstalled); err != nil {
		t.Fatalf("bound device signs in again after log out: %v", err)
	}
	f.assertDevices(t, u.ID, 1)
	f.assertBindings(t, u.ID, 1)
}

func TestDeviceLimitBelowCurrentDevicesKeepsThemAndRejectsNewLogins(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105005"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 3)
	keys := make([][8]byte, 0, 3)
	for i := byte(1); i <= 3; i++ {
		key := f.device(t, i)
		keys = append(keys, key)
		if err := f.signInOn(t, phone, key); err != nil {
			t.Fatalf("sign in on device %d: %v", i, err)
		}
	}
	f.setLimit(t, u.ID, 1)
	f.assertDevices(t, u.ID, 3)
	f.assertBindings(t, u.ID, 3)
	for i, key := range keys {
		if id, found, err := f.svc.UserID(context.Background(), key); err != nil || !found || id != u.ID {
			t.Fatalf("device %d after lowering the limit user=%d found=%v err=%v, want still signed in", i+1, id, found, err)
		}
	}
	if err := f.signInOn(t, phone, f.device(t, 4)); !errors.Is(err, domain.ErrDeviceLimitReached) {
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
		if _, err := f.svc.SignInBot(ctx, domain.Authorization{AuthKeyID: f.key(t, i, "")}, token); err != nil {
			t.Fatalf("bot sign in on key %d: %v", i, err)
		}
	}
	f.assertDevices(t, bot.ID, 3)
	f.assertBindings(t, bot.ID, 0)
}

// 设备绑定（#107、#112）：登录密码（FinishVerifiedSignIn）、Passkey（BindVerifiedLogin）、扫码确认
// （AcceptLoginToken）与验证码登录走同一个绑定步骤：名额满时返回 domain.ErrDeviceLimitReached，
// 没有 device_id 时返回 domain.ErrDeviceIDRequired，新设备都不会被绑定。
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
			noID := f.key(t, 9, "")
			if err := entry.login(f, u, noID); !errors.Is(err, domain.ErrDeviceIDRequired) {
				t.Fatalf("device without a device id err=%v, want ErrDeviceIDRequired", err)
			}
			f.assertNotSignedIn(t, noID)
			if err := entry.login(f, u, f.device(t, 1)); err != nil {
				t.Fatalf("first device below the limit: %v", err)
			}
			second := f.device(t, 2)
			if err := entry.login(f, u, second); !errors.Is(err, domain.ErrDeviceLimitReached) {
				t.Fatalf("second device err=%v, want ErrDeviceLimitReached", err)
			}
			f.assertDevices(t, u.ID, 1)
			f.assertBindings(t, u.ID, 1)
			f.assertNotSignedIn(t, second)
		})
	}
}
