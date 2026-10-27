package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestChannelMessageForwardsPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.ChannelMessageForwards(t, func(t *testing.T) storetest.ChannelMessageForwardsFixture {
		ctx := context.Background()
		users := NewUserStore(pool)
		suffix := randomSuffix(t)
		owner, err := users.Create(ctx, domain.User{AccessHash: 1401, Phone: "+178" + suffix + "01", FirstName: "ForwardsOwner"})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := users.Create(ctx, domain.User{AccessHash: 1402, Phone: "+178" + suffix + "02", FirstName: "ForwardsReader"})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.ChannelMessageForwardsFixture{
			Messages: newTestMessageStore(pool),
			Channels: newTestChannelStore(pool),
			Owner:    owner.ID,
			Reader:   reader.ID,
		}
	})
}
