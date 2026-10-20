package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelSenderBansPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 441, Phone: "+188" + suffix + "01", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 442, Phone: "+188" + suffix + "02", FirstName: "Member"})
	if err != nil {
		t.Fatal(err)
	}
	storetest.ChannelSenderBans(t, NewChannelStore(pool), func() store.ChannelStore { return NewChannelStore(pool) }, owner.ID, member.ID)
	t.Run("linked discussion auto-forward", func(t *testing.T) { storetest.ChannelSenderBanLinkedDiscussion(t, NewChannelStore(pool), owner.ID) })
}
