package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// 设备绑定与两步验证（#106、#112）：输完验证码停在 2FA 的授权不判、不绑定、不占名额，
// 即使名额已满也先进入 2FA；2FA 完成转正时才按绑定规则绑定或拒绝，被拒时删除这条待完成的授权。

func (f deviceLimitFixture) enableTwoFactor(t *testing.T, userID int64) {
	t.Helper()
	if err := f.passwords.Save(context.Background(), userID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("enable 2FA: %v", err)
	}
}

// signInToTwoFactorOn 在 key 上输完验证码，期望停在 2FA 步骤。
func (f deviceLimitFixture) signInToTwoFactorOn(t *testing.T, phone string, key [8]byte) {
	t.Helper()
	if err := f.signInOn(t, phone, key); !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		t.Fatalf("sign in on %x err=%v, want ErrSessionPasswordNeeded", key, err)
	}
}

func TestDeviceLimitTwoFactorRejectsANewDeviceOnlyAtCompletion(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106001"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	if err := f.signInOn(t, phone, f.device(t, 1)); err != nil {
		t.Fatalf("first device: %v", err)
	}
	f.enableTwoFactor(t, u.ID)
	second := f.device(t, 2)
	// 名额已满，验证码这一步仍放行进入 2FA。
	f.signInToTwoFactorOn(t, phone, second)
	f.assertBindings(t, u.ID, 1)
	if err := f.svc.CompletePasswordSignIn(ctx, second, u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("complete 2FA on a new device at the limit err=%v, want ErrDeviceLimitReached", err)
	}
	if _, found, err := f.svc.PendingPasswordUserID(ctx, second); err != nil || found {
		t.Fatalf("rejected device pending=%v err=%v, want deleted", found, err)
	}
	f.assertDevices(t, u.ID, 1)
	f.assertBindings(t, u.ID, 1)
}

func TestDeviceLimitTwoFactorWithoutDeviceIDIsRejectedAtCompletion(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106005"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 2)
	f.enableTwoFactor(t, u.ID)
	key := f.key(t, 1, "")
	f.signInToTwoFactorOn(t, phone, key)
	if err := f.svc.CompletePasswordSignIn(ctx, key, u.ID); !errors.Is(err, domain.ErrDeviceIDRequired) {
		t.Fatalf("complete 2FA without a device id err=%v, want ErrDeviceIDRequired", err)
	}
	f.assertNotSignedIn(t, key)
	f.assertBindings(t, u.ID, 0)
}

func TestDeviceLimitOnlyFirstOfTwoPendingTwoFactorDevicesCompletes(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106003"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 2)
	if err := f.signInOn(t, phone, f.device(t, 1)); err != nil {
		t.Fatalf("first device: %v", err)
	}
	f.enableTwoFactor(t, u.ID)
	second, third := f.device(t, 2), f.device(t, 3)
	f.signInToTwoFactorOn(t, phone, second)
	f.signInToTwoFactorOn(t, phone, third)

	if err := f.svc.CompletePasswordSignIn(ctx, third, u.ID); err != nil {
		t.Fatalf("first to complete 2FA: %v", err)
	}
	if err := f.svc.CompletePasswordSignIn(ctx, second, u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("second to complete 2FA err=%v, want ErrDeviceLimitReached", err)
	}
	f.assertNotSignedIn(t, second)
	if _, found, err := f.svc.UserID(ctx, second); err != nil || found {
		t.Fatalf("rejected device signed in=%v err=%v, want signed out", found, err)
	}
	f.assertDevices(t, u.ID, 2)
	f.assertBindings(t, u.ID, 2)
}

// CheckDeviceLimit 是 2FA 收尾前的只读预检（密码找回会先改掉 2FA 密码，必须在那之前拒绝）：
// 与转正同一套规则，名额已满或没有 device_id 时报告拒绝，且不删除、不转正这条待完成的授权。
func TestDeviceLimitCheckBeforePasswordRecoveryIsReadOnly(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106004"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	f.enableTwoFactor(t, u.ID)
	first := f.device(t, 1)
	f.signInToTwoFactorOn(t, phone, first)
	if err := f.svc.CheckDeviceLimit(ctx, first, u.ID); err != nil {
		t.Fatalf("check with a free slot: %v", err)
	}
	f.assertBindings(t, u.ID, 0)
	noID := f.key(t, 9, "")
	f.signInToTwoFactorOn(t, phone, noID)
	if err := f.svc.CheckDeviceLimit(ctx, noID, u.ID); !errors.Is(err, domain.ErrDeviceIDRequired) {
		t.Fatalf("check without a device id err=%v, want ErrDeviceIDRequired", err)
	}
	second := f.device(t, 2)
	f.signInToTwoFactorOn(t, phone, second)
	if err := f.svc.CompletePasswordSignIn(ctx, second, u.ID); err != nil {
		t.Fatalf("complete 2FA on the second device: %v", err)
	}
	if err := f.svc.CheckDeviceLimit(ctx, first, u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("check with the slots full err=%v, want ErrDeviceLimitReached", err)
	}
	for _, key := range [][8]byte{first, noID} {
		if id, found, err := f.svc.PendingPasswordUserID(ctx, key); err != nil || !found || id != u.ID {
			t.Fatalf("pending device %x after check user=%d found=%v err=%v, want still pending", key, id, found, err)
		}
	}
}
