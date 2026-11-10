package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestChannelImportPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.ChannelImport(t, func(t *testing.T) storetest.ChannelImportFixture {
		ctx := context.Background()
		users := NewUserStore(pool)
		suffix := randomSuffix(t)
		owner, err := users.Create(ctx, domain.User{AccessHash: 1501, Phone: "+179" + suffix + "01", FirstName: "ImportOwner"})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := users.Create(ctx, domain.User{AccessHash: 1502, Phone: "+179" + suffix + "02", FirstName: "ImportReader"})
		if err != nil {
			t.Fatal(err)
		}
		channels := newTestChannelStore(pool)
		return storetest.ChannelImportFixture{
			Channels: channels,
			Imports:  channels,
			Owner:    owner.ID,
			Reader:   reader.ID,
		}
	})
}
