package postgres

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelInvitePermissionsPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 5)
	storetest.ChannelInvitePermissions(t, NewChannelStore(pool), [5]int64{ids[0], ids[1], ids[2], ids[3], ids[4]})
}

func TestChannelInviteParameterValidationPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 1)
	storetest.ChannelInviteParameterValidation(t, NewChannelStore(pool), ids[0])
}
