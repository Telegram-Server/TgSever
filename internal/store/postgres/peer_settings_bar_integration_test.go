package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestPeerSettingsBarContractPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createRevokeTestUser(t, ctx, pool, "bar-a")
	other := createRevokeTestUser(t, ctx, pool, "bar-b")
	storetest.PeerSettingsBarContract(t, NewDialogStore(pool), user, other)
}
