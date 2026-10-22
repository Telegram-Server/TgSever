package rpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	tdsrp "github.com/iamxvbaba/td/crypto/srp"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	accountapp "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountPasswordWithoutPassportKeepsWorkingSRP(t *testing.T) {
	const userID int64 = 42001
	ctx := WithUserID(context.Background(), userID)
	account := accountapp.NewService(memory.NewPasswordStore())
	initial, err := account.GetPassword(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	password := []byte("normal desktop password")
	input := tdsrp.Input{Salt1: initial.NewAlgo.Salt1, Salt2: initial.NewAlgo.Salt2, G: initial.NewAlgo.G, P: initial.NewAlgo.P}
	verifier, salt, err := tdsrp.NewSRP(bytes.NewReader(bytes.Repeat([]byte{0xa7}, 32))).NewHash(password, input)
	if err != nil {
		t.Fatal(err)
	}
	algo := initial.NewAlgo
	algo.Salt1 = salt
	if err := account.UpdatePasswordSettings(ctx, userID, domain.PasswordCheck{Empty: true}, domain.PasswordInputSettings{NewAlgo: &algo, NewPasswordHash: verifier, Hint: "desktop hint", HasHint: true, Email: "desktop@example.test", HasEmail: true}); err != nil {
		t.Fatal(err)
	}
	router := New(Config{DC: 2}, Deps{Account: account}, zaptest.NewLogger(t), clock.System)
	settings, err := router.onAccountGetPassword(ctx)
	if err != nil {
		t.Fatalf("ordinary getPassword without Passport storage: %v", err)
	}
	if !settings.HasPassword || settings.HasSecureValues || settings.Hint != "desktop hint" {
		t.Fatalf("ordinary password settings changed: password=%v secure=%v hint=%q", settings.HasPassword, settings.HasSecureValues, settings.Hint)
	}
	current, ok := settings.CurrentAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
	if !ok {
		t.Fatalf("password algorithm = %T", settings.CurrentAlgo)
	}
	answer, err := tdsrp.NewSRP(nil).Hash(password, settings.SRPB, bytes.Repeat([]byte{0x23}, 256), tdsrp.Input{Salt1: current.Salt1, Salt2: current.Salt2, G: current.G, P: current.P})
	if err != nil {
		t.Fatal(err)
	}
	private, err := router.onAccountGetPasswordSettings(ctx, &tg.InputCheckPasswordSRP{SRPID: settings.SRPID, A: answer.A, M1: answer.M1})
	if err != nil {
		t.Fatalf("returned challenge cannot authenticate the existing password: %v", err)
	}
	if private.Email != "desktop@example.test" {
		t.Fatalf("authenticated settings lost recovery email: %q", private.Email)
	}
	if _, present := private.GetSecureSettings(); present {
		t.Fatal("ordinary account acquired invented Passport secure settings")
	}
}
