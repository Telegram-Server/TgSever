package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/admin"
	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestAdminCreateAccountCommandAuditPostgres 后台建号命令在真实存储上：注册关闭时仍能建号，
// 试运行不建号，同一 command id 重放不建第二个号，每次（含号码冲突的失败）都写入命令日志和审计日志，
// 建出的账号没有任何授权。
func TestAdminCreateAccountCommandAuditPostgres(t *testing.T) {
	pool := testTx(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	authorizations := NewAuthorizationStore(pool)
	authSvc := appauth.NewService(users, authorizations, memory.NewCodeStore(), nil, nil, "12345",
		appauth.WithSignUpAllowed(appauth.StaticSignUpAllowed(false)),
		appauth.WithPremiumGrant(3),
	)
	svc := admin.NewService(admin.Dependencies{Commands: NewAdminStore(pool), AccountCreator: authSvc})
	const phone = "15550007841"

	auditRow := func(commandID string) (action, status, actor, reason string, dryRun bool, n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM admin_audit_logs WHERE command_id = $1`, commandID).Scan(&n); err != nil {
			t.Fatalf("count audit %s: %v", commandID, err)
		}
		if n == 0 {
			return
		}
		if err := pool.QueryRow(ctx, `SELECT action, status, actor, reason, dry_run FROM admin_audit_logs WHERE command_id = $1`, commandID).
			Scan(&action, &status, &actor, &reason, &dryRun); err != nil {
			t.Fatalf("read audit %s: %v", commandID, err)
		}
		return
	}
	commandStatus := func(commandID string) string {
		t.Helper()
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM admin_commands WHERE command_id = $1`, commandID).Scan(&status); err != nil {
			t.Fatalf("read command %s: %v", commandID, err)
		}
		return status
	}

	dry, err := svc.CreateAccount(ctx, admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "dry-pg-create-account", Actor: "ops", Reason: "new hire", DryRun: true},
		Phone:       "+" + phone, FirstName: "Ivy",
	})
	if err != nil || !dry.DryRun || dry.Details["phone"] != phone {
		t.Fatalf("dry run result=%+v err=%v", dry, err)
	}
	if _, found, _ := users.ByPhone(ctx, phone); found {
		t.Fatal("dry run created an account")
	}
	if action, status, _, _, dryRun, n := auditRow("dry-pg-create-account"); n != 1 || action != admin.ActionCreateAccount || status != "completed" || !dryRun {
		t.Fatalf("dry run audit n=%d action=%s status=%s dry=%v", n, action, status, dryRun)
	}

	execReq := admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "exec-pg-create-account", Actor: "ops", Reason: "new hire"},
		Phone:       "+" + phone, FirstName: "Ivy", LastName: "Ops",
	}
	exec, err := svc.CreateAccount(ctx, execReq)
	if err != nil || exec.Status != "completed" {
		t.Fatalf("execute result=%+v err=%v", exec, err)
	}
	created, found, err := users.ByPhone(ctx, phone)
	if err != nil || !found || created.FirstName != "Ivy" || created.LastName != "Ops" {
		t.Fatalf("created user=%+v found=%v err=%v", created, found, err)
	}
	if exec.Details["user_id"] != created.ID {
		t.Fatalf("result user_id=%v, want %d", exec.Details["user_id"], created.ID)
	}
	if !created.PremiumActiveAt(time.Now().AddDate(0, 2, 0).Unix()) {
		t.Fatalf("PremiumUntil=%d, want the sign-up premium grant", created.PremiumUntil)
	}
	auths, err := authorizations.ListByUser(ctx, created.ID)
	if err != nil || len(auths) != 0 {
		t.Fatalf("authorizations=%d err=%v, want none", len(auths), err)
	}
	if action, status, actor, reason, dryRun, n := auditRow("exec-pg-create-account"); n != 1 || action != admin.ActionCreateAccount || status != "completed" || actor != "ops" || reason != "new hire" || dryRun {
		t.Fatalf("execute audit n=%d action=%s status=%s actor=%s reason=%s dry=%v", n, action, status, actor, reason, dryRun)
	}

	replay, err := svc.CreateAccount(ctx, execReq)
	if err != nil || !replay.AlreadyExecuted {
		t.Fatalf("replay result=%+v err=%v, want already executed", replay, err)
	}
	var accounts int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM users WHERE phone = $1`, phone).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("accounts with phone=%d err=%v, want exactly one", accounts, err)
	}
	if _, _, _, _, _, n := auditRow("exec-pg-create-account"); n != 1 {
		t.Fatalf("audit rows after replay=%d, want 1", n)
	}

	occupied, err := svc.CreateAccount(ctx, admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "dup-pg-create-account", Actor: "ops", Reason: "retry with new id"},
		Phone:       phone, FirstName: "Ivy",
	})
	if !errors.Is(err, domain.ErrPhoneNumberOccupied) || occupied.Code != "PHONE_NUMBER_OCCUPIED" {
		t.Fatalf("occupied result=%+v err=%v, want PHONE_NUMBER_OCCUPIED", occupied, err)
	}
	if status := commandStatus("dup-pg-create-account"); status != "failed" {
		t.Fatalf("occupied command status=%s, want failed", status)
	}
	if _, status, _, _, _, n := auditRow("dup-pg-create-account"); n != 1 || status != "failed" {
		t.Fatalf("occupied audit n=%d status=%s, want one failed row", n, status)
	}
}
