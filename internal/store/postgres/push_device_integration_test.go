package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/deploy"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestPushDeviceContractPostgres(t *testing.T) {
	pool := testPool(t)
	devices := NewPushDeviceStore(pool)
	auths := NewAuthorizationStore(pool)
	keys := NewAuthKeyStore(pool)
	lifecycle := NewAccountLifecycleStore(pool)
	var seed byte
	storetest.PushDeviceContract(t, func(t *testing.T) storetest.PushDeviceFixture {
		ctx := context.Background()
		userA := createRevokeTestUser(t, ctx, pool, "push-a")
		userB := createRevokeTestUser(t, ctx, pool, "push-b")
		next := func() [8]byte {
			seed++
			return [8]byte{0x94, seed, 0x94, seed, 0x94, seed, 0x94, seed}
		}
		f := storetest.PushDeviceFixture{
			Devices: devices,
			Auths:   auths,
			UserA:   userA, UserB: userB,
			AuthA1: next(), AuthA2: next(), AuthB: next(),
			HashA1: time.Now().UnixNano(), HashA2: time.Now().UnixNano() + 1,
			DeleteAuthKey: keys.Delete,
			DeleteAccount: func(ctx context.Context, userID int64) error {
				_, err := lifecycle.ExecuteAccountDeletion(ctx, userID, domain.AccountDeletionManual, "", time.Now())
				return err
			},
		}
		for _, bind := range []domain.Authorization{
			{AuthKeyID: f.AuthA1, UserID: userA, Hash: f.HashA1},
			{AuthKeyID: f.AuthA2, UserID: userA, Hash: f.HashA2},
			{AuthKeyID: f.AuthB, UserID: userB, Hash: f.HashA2 + 1},
		} {
			saveRevokeTestAuthKey(t, ctx, keys, bind.AuthKeyID, 0)
			if err := auths.Bind(ctx, bind); err != nil {
				t.Fatalf("bind authorization: %v", err)
			}
		}
		return f
	})
}

// 迁移可以反向再正向执行：在事务里跑 down、确认表已删除，再跑 up、确认表恢复，最后回滚。
func TestPushDevicesMigrationReversiblePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const name = "migrations/20261024094000_push_devices"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tableExists := func() bool {
		t.Helper()
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('public.push_devices') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	for _, step := range []struct {
		file string
		want bool
	}{{name + ".down.sql", false}, {name + ".up.sql", true}} {
		body, err := deploy.Migrations.ReadFile(step.file)
		if err != nil {
			t.Fatalf("read %s: %v", step.file, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			t.Fatalf("exec %s: %v", step.file, err)
		}
		if got := tableExists(); got != step.want {
			t.Fatalf("after %s push_devices exists=%v, want %v", step.file, got, step.want)
		}
	}
}
