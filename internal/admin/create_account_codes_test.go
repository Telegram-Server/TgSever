package admin

import (
	"context"
	"testing"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// The panel shows a translated message per rejection code, so every rejection
// an operator can fix by retyping the number carries a stable code, with the
// real auth service as the account creator (its errors are what the panel
// actually sees). A reserved system number gets its own code: unlike the
// client-facing RPC, which folds it into PHONE_NUMBER_INVALID so as not to
// reveal which numbers are system accounts, the operator needs to know why.
func TestCreateAccountRejectionCodesFromTheAuthService(t *testing.T) {
	ctx := context.Background()
	authSvc := appauth.NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), AccountCreator: authSvc})

	cases := []struct {
		name, phone, firstName, code string
	}{
		{"invalid phone", "not-a-phone", "Eve", "PHONE_NUMBER_INVALID"},
		{"reserved system number", "+" + domain.OfficialSystemPhone, "Eve", "PHONE_NUMBER_RESERVED"},
		{"missing first name", "15550007899", "  ", "FIRSTNAME_INVALID"},
	}
	for _, tc := range cases {
		for _, dryRun := range []bool{true, false} {
			id := tc.code + "-exec"
			if dryRun {
				id = tc.code + "-dry"
			}
			res, err := svc.CreateAccount(ctx, CreateAccountRequest{
				CommandMeta: CommandMeta{CommandID: id, Actor: "ops", Reason: "new hire", DryRun: dryRun},
				Phone:       tc.phone,
				FirstName:   tc.firstName,
			})
			if err == nil || res.Status != string(domain.AdminCommandFailed) || res.Code != tc.code {
				t.Fatalf("%s dryRun=%v: result=%+v err=%v, want failed with %s", tc.name, dryRun, res, err, tc.code)
			}
		}
	}
}
