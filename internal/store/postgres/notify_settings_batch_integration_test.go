package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestNotifySettingsBatchContractPostgres(t *testing.T) {
	pool := testTx(t)
	ctx := context.Background()
	owners := [3]int64{
		createRevokeTestUser(t, ctx, pool, "notify-batch-a"),
		createRevokeTestUser(t, ctx, pool, "notify-batch-b"),
		createRevokeTestUser(t, ctx, pool, "notify-batch-c"),
	}
	storetest.NotifySettingsBatchContract(t, NewPasswordStore(pool), owners)
}
