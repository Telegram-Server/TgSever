package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelStickerSetsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	ids := make([]int64, 150)
	suffix := randomSuffix(t)
	for i := range ids {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(i + 1), Phone: fmt.Sprintf("+155%s%03d", suffix, i), FirstName: "Sticker member"})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = u.ID
	}
	storetest.ChannelStickerSets(t, NewChannelStore(pool), func() store.ChannelStore { return NewChannelStore(pool) }, ids)
}
