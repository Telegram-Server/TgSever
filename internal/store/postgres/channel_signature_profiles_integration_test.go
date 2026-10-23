package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelSignatureProfilesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 991, Phone: "+177" + suffix + "01", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 992, Phone: "+177" + suffix + "02", FirstName: "Member"})
	if err != nil {
		t.Fatal(err)
	}
	storetest.ChannelSignatureProfiles(t, NewChannelStore(pool), func() store.ChannelStore { return NewChannelStore(pool) }, owner.ID, member.ID)
}
