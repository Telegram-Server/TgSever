package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotForumPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	ids := make([]int64, 4)
	for i := range ids {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(101 + i), Phone: fmt.Sprintf("+155%s%03d", suffix, i), FirstName: "Forum member"})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = u.ID
	}
	storetest.BotForum(t, NewChannelStore(pool), func() store.ChannelStore { return NewChannelStore(pool) }, ids)
}
