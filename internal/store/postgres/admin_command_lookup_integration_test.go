package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// Command 读出 BeginCommand 写下的记录；没有记录时 found=false 而不是报错。
func TestAdminCommandLookupPostgres(t *testing.T) {
	store := NewAdminStore(testPool(t))
	ctx := context.Background()
	if _, found, err := store.Command(ctx, "lookup-missing"); err != nil || found {
		t.Fatalf("missing command found=%v err=%v", found, err)
	}
	if _, created, err := store.BeginCommand(ctx, domain.AdminCommand{
		CommandID: "lookup-present", Actor: "ops", Action: "account.set_permissions", TargetUserID: 1999999882, Reason: "staff account",
		RequestJSON: []byte(`{"user_id":1999999882}`), Status: domain.AdminCommandRunning,
	}); err != nil || !created {
		t.Fatalf("BeginCommand created=%v err=%v", created, err)
	}
	cmd, found, err := store.Command(ctx, "lookup-present")
	if err != nil || !found || cmd.Action != "account.set_permissions" || cmd.TargetUserID != 1999999882 || cmd.Status != domain.AdminCommandRunning {
		t.Fatalf("Command = %+v found=%v err=%v", cmd, found, err)
	}
}
