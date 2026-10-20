package admin

import (
	"context"
	"testing"
)

// 建号带密码的幂等指纹按规范化后的手机号计算（与建号实际使用的规范化一致）：同一个号码的
// 不同写法（带 +、空格、横线）对应同一个账号，指纹也相同。
func TestCreateAccountPasswordFingerprintUsesTheNormalizedPhone(t *testing.T) {
	f := newCreateAccountPasswordFixture(t)
	ctx := context.Background()
	const password = "same-password"

	plain := createAccountWithPasswordRequest("dry-create-pw-phone-a", "15550090031", password)
	plain.DryRun = true
	spelled := createAccountWithPasswordRequest("dry-create-pw-phone-b", "+1 555-009-0031", password)
	spelled.DryRun = true
	for _, req := range []CreateAccountRequest{plain, spelled} {
		if _, err := f.svc.CreateAccount(ctx, req); err != nil {
			t.Fatalf("%s: %v", req.CommandID, err)
		}
	}
	a := storedFingerprint(t, f.repo.items[plain.CommandID])
	b := storedFingerprint(t, f.repo.items[spelled.CommandID])
	if a != b {
		t.Fatalf("fingerprints differ for two spellings of one number: %s vs %s", a, b)
	}
}
