package postgres

import (
	"context"
	"testing"

	"telesrv/internal/store/storetest"
)

func TestDialogFolderLimitContractPostgres(t *testing.T) {
	pool := testPool(t)
	dialogs := NewDialogStore(pool)
	storetest.DialogFolderLimitContract(t, func(t *testing.T) storetest.DialogFolderLimitFixture {
		ctx := context.Background()
		return storetest.DialogFolderLimitFixture{
			Dialogs: dialogs,
			UserA:   createRevokeTestUser(t, ctx, pool, "folders-a"),
			UserB:   createRevokeTestUser(t, ctx, pool, "folders-b"),
		}
	})
}
