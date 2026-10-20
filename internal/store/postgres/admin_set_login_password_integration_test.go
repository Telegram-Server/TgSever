package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestAdminSetLoginPasswordCommandAuditPostgres 后台设置登录密码命令在真实存储上：试运行不写密码，
// 执行后登录密码落库，命令表和审计表的 request/result/error 里都没有明文密码，也没有不带密钥的摘要。
func TestAdminSetLoginPasswordCommandAuditPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	passwords := NewLoginPasswordStore(pool)
	setter := loginpassword.NewService(users, passwords, memory.NewLoginPasswordChallengeStore(time.Now), nil)
	svc := admin.NewService(admin.Dependencies{
		Commands:                    NewAdminStore(pool),
		LoginPasswords:              setter,
		LoginPasswordFingerprintKey: admin.DeriveLoginPasswordFingerprintKey("pg-admin-token"),
	})
	u, err := users.Create(ctx, domain.User{Phone: "15550088841", FirstName: "Pg"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const password = "pg-plaintext-marker-7f3a"

	dry := admin.SetLoginPasswordRequest{
		CommandMeta: admin.CommandMeta{CommandID: "dry-pg-set-login-password", Actor: "ops", Reason: "forgot password", DryRun: true},
		UserID:      u.ID,
		Password:    password,
	}
	if res, err := svc.SetLoginPassword(ctx, dry); err != nil || !res.DryRun {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if _, has, err := passwords.GetLoginPassword(ctx, u.ID); err != nil || has {
		t.Fatalf("after the dry run has=%v err=%v, want no login password", has, err)
	}
	exec := dry
	exec.CommandID = "exec-pg-set-login-password"
	exec.DryRun = false
	exec.RevokeSessions = false
	if res, err := svc.SetLoginPassword(ctx, exec); err != nil || res.Status != "completed" {
		t.Fatalf("execute = %+v, %v", res, err)
	}
	if _, has, err := passwords.GetLoginPassword(ctx, u.ID); err != nil || !has {
		t.Fatalf("after execute has=%v err=%v, want a login password", has, err)
	}

	sum := sha256.Sum256([]byte(password))
	for _, id := range []string{dry.CommandID, exec.CommandID} {
		var commandRow, auditRow string
		if err := pool.QueryRow(ctx, `SELECT request::text || result::text || coalesce(error, '') FROM admin_commands WHERE command_id = $1`, id).Scan(&commandRow); err != nil {
			t.Fatalf("read command %s: %v", id, err)
		}
		if err := pool.QueryRow(ctx, `SELECT action || request::text || result::text FROM admin_audit_logs WHERE command_id = $1`, id).Scan(&auditRow); err != nil {
			t.Fatalf("read audit %s: %v", id, err)
		}
		if !strings.HasPrefix(auditRow, admin.ActionSetLoginPassword) {
			t.Fatalf("audit %s = %s, want action %s", id, auditRow, admin.ActionSetLoginPassword)
		}
		for _, row := range []string{commandRow, auditRow} {
			if strings.Contains(row, password) || strings.Contains(row, hex.EncodeToString(sum[:])) {
				t.Fatalf("%s recorded the password or an unkeyed digest: %s", id, row)
			}
		}
	}
}
