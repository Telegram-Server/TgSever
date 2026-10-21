package main

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/store/postgres/sqlcgen"
)

// 账号详情读取设备上限（#108）。上限存在 account_restrictions.device_limit，与冻结状态同一行，
// 所以要对真实 schema 证明三件事：没设上限时是 nil（面板显示"不限制"），设了以后读出原值，
// 只为上限建出来的那一行不能被当成"账号受限制"。
func TestReadStoreAccountDetailCarriesDeviceLimit(t *testing.T) {
	store, pool := verificationReadStore(t)
	ctx := context.Background()
	userID := 3_700_000_000 + time.Now().UnixNano()%1_000_000

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_restrictions WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, created_at, updated_at)
VALUES ($1, $2, '', 'Staff', '', '', now(), now())`, userID, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	unset, err := store.AccountDetail(ctx, userID)
	if err != nil {
		t.Fatalf("AccountDetail without a limit: %v", err)
	}
	if unset.DeviceLimit != nil {
		t.Fatalf("DeviceLimit without a limit = %d, want nil (unlimited)", *unset.DeviceLimit)
	}

	// 用写路径同一条 sqlc 语句设上限：账号此前没有 account_restrictions 行，这一行只为上限而生。
	limit := int32(3)
	if err := sqlcgen.New(pool).SetDeviceLimit(ctx, sqlcgen.SetDeviceLimitParams{UserID: userID, DeviceLimit: &limit}); err != nil {
		t.Fatalf("set device limit: %v", err)
	}
	limited, err := store.AccountDetail(ctx, userID)
	if err != nil {
		t.Fatalf("AccountDetail with a limit: %v", err)
	}
	if limited.DeviceLimit == nil || *limited.DeviceLimit != 3 {
		t.Fatalf("DeviceLimit = %v, want 3", limited.DeviceLimit)
	}
	if limited.HasRestriction || limited.Account.Frozen {
		t.Fatalf("a limit-only row reads as a restriction: HasRestriction=%v Frozen=%v", limited.HasRestriction, limited.Account.Frozen)
	}

	if _, err := sqlcgen.New(pool).ClearDeviceLimit(ctx, userID); err != nil {
		t.Fatalf("clear device limit: %v", err)
	}
	cleared, err := store.AccountDetail(ctx, userID)
	if err != nil {
		t.Fatalf("AccountDetail after clearing: %v", err)
	}
	if cleared.DeviceLimit != nil {
		t.Fatalf("DeviceLimit after clearing = %d, want nil", *cleared.DeviceLimit)
	}
}
