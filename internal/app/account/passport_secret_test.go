package account

import (
	"bytes"
	"context"
	"errors"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestPassportSecretRequiresSRPAndPreservesPassword(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 7744
	passwords := memory.NewPasswordStore()
	svc := NewService(passwords)
	initial, err := svc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	algo := initial.NewAlgo
	algo.Salt1 = append(append([]byte(nil), algo.Salt1...), bytes.Repeat([]byte{9}, 32)...)
	password := []byte("passport password")
	if err = svc.UpdatePasswordSettings(ctx, userID, domain.PasswordCheck{Empty: true}, domain.PasswordInputSettings{NewAlgo: &algo, NewPasswordHash: verifierForPassword(t, algo, password)}); err != nil {
		t.Fatal(err)
	}
	challenge, err := svc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	check := clientPasswordCheck(t, challenge, password)
	secret := &domain.PassportSecret{ID: 8811, Algo: "pbkdf2_hmac_sha512_iter100000", Salt: append(append([]byte(nil), challenge.NewSecureAlgo.Salt...), bytes.Repeat([]byte{10}, 32)...), Secret: bytes.Repeat([]byte{11}, 32)}
	bad := check
	bad.M1 = append([]byte(nil), check.M1...)
	bad.M1[0] ^= 1
	if err = svc.UpdatePasswordSettings(ctx, userID, bad, domain.PasswordInputSettings{NewSecureSettings: secret}); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("bad SRP set secret: %v", err)
	}
	before, _, err := passwords.GetByUser(ctx, userID)
	if err != nil || before.SecureSettings != nil {
		t.Fatalf("failed SRP persisted secret: %+v %v", before.SecureSettings, err)
	}
	if err = svc.UpdatePasswordSettings(ctx, userID, check, domain.PasswordInputSettings{NewSecureSettings: secret}); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.GetPasswordSettings(ctx, userID, check)
	if err != nil || saved.SecureSettings == nil || saved.SecureSettings.ID != secret.ID || !bytes.Equal(saved.SecureSettings.Secret, secret.Secret) {
		t.Fatalf("opaque secret=%+v %v", saved.SecureSettings, err)
	}
	if err = svc.CheckPassword(ctx, userID, check); err != nil {
		t.Fatalf("secure-only change cleared the password: %v", err)
	}
}
