package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// 设备上限与两步验证（#106）：输完验证码时名额已满直接拒绝、不进入 2FA；停在 2FA 的授权
// 不占名额；2FA 完成转正时再检查一次，超限则拒绝并删除这条待完成的授权。

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

func TestDeviceLimitRejectsTwoFactorAccountAtTheCodeStep(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550106001"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	if err := f.signInOn(t, phone, deviceKey(1)); err != nil {
		t.Fatalf("first device: %v", err)
	}
	f.enableTwoFactor(t, u.ID)
	if err := f.signInOn(t, phone, deviceKey(2)); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("code step with the slots full err=%v, want ErrDeviceLimitReached", err)
	}
	if _, found, err := f.svc.PendingPasswordUserID(context.Background(), deviceKey(2)); err != nil || found {
		t.Fatalf("rejected device pending=%v err=%v, want no 2FA step", found, err)
	}
	f.assertDevices(t, u.ID, 1)
}

func TestDeviceLimitPendingTwoFactorTakesNoSlot(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106002"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	f.enableTwoFactor(t, u.ID)
	f.signInToTwoFactorOn(t, phone, deviceKey(1))
	f.signInToTwoFactorOn(t, phone, deviceKey(2))
	if err := f.svc.CompletePasswordSignIn(ctx, deviceKey(2), u.ID); err != nil {
		t.Fatalf("complete 2FA while another device is pending: %v", err)
	}
	if id, found, err := f.svc.UserID(ctx, deviceKey(2)); err != nil || !found || id != u.ID {
		t.Fatalf("completed device user=%d found=%v err=%v, want signed in", id, found, err)
	}
}

func TestDeviceLimitOnlyFirstOfTwoPendingTwoFactorDevicesCompletes(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106003"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 2)
	if err := f.signInOn(t, phone, deviceKey(1)); err != nil {
		t.Fatalf("first device: %v", err)
	}
	f.enableTwoFactor(t, u.ID)
	f.signInToTwoFactorOn(t, phone, deviceKey(2))
	f.signInToTwoFactorOn(t, phone, deviceKey(3))

	if err := f.svc.CompletePasswordSignIn(ctx, deviceKey(3), u.ID); err != nil {
		t.Fatalf("first to complete 2FA: %v", err)
	}
	if err := f.svc.CompletePasswordSignIn(ctx, deviceKey(2), u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("second to complete 2FA err=%v, want ErrDeviceLimitReached", err)
	}
	if _, found, err := f.svc.Authorization(ctx, deviceKey(2)); err != nil || found {
		t.Fatalf("rejected pending authorization found=%v err=%v, want deleted", found, err)
	}
	if _, found, err := f.svc.UserID(ctx, deviceKey(2)); err != nil || found {
		t.Fatalf("rejected device signed in=%v err=%v, want signed out", found, err)
	}
	f.assertDevices(t, u.ID, 2)
}

// CheckDeviceLimit 是 2FA 收尾前的只读预检（密码找回会先改掉 2FA 密码，必须在那之前拒绝）：
// 名额已满时报告超限，且不删除、不转正这条待完成的授权。
func TestDeviceLimitCheckBeforePasswordRecoveryIsReadOnly(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	const phone = "15550106004"
	u := f.existingUser(t, phone)
	f.setLimit(t, u.ID, 1)
	f.enableTwoFactor(t, u.ID)
	f.signInToTwoFactorOn(t, phone, deviceKey(1))
	if err := f.svc.CheckDeviceLimit(ctx, deviceKey(1), u.ID); err != nil {
		t.Fatalf("check with a free slot: %v", err)
	}
	f.signInToTwoFactorOn(t, phone, deviceKey(2))
	if err := f.svc.CompletePasswordSignIn(ctx, deviceKey(2), u.ID); err != nil {
		t.Fatalf("complete 2FA on the second device: %v", err)
	}
	if err := f.svc.CheckDeviceLimit(ctx, deviceKey(1), u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
		t.Fatalf("check with the slots full err=%v, want ErrDeviceLimitReached", err)
	}
	if id, found, err := f.svc.PendingPasswordUserID(ctx, deviceKey(1)); err != nil || !found || id != u.ID {
		t.Fatalf("pending device after check user=%d found=%v err=%v, want still pending", id, found, err)
	}
}
