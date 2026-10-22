package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestPrivateForumLifecycleIsolation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	ids := make([]int64, 0, 3)
	for _, item := range []struct{ phone, name string }{{"01", "TopicBot"}, {"02", "TopicPeer"}, {"03", "OtherPeer"}} {
		user, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1987" + suffix + item.phone, FirstName: item.name})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, user.ID)
	}
	storetest.PrivateForum(t, NewMessageStore(pool), func() store.MessageStore { return NewMessageStore(pool) }, NewChannelStore(pool), ids[0], ids[1], ids[2])
}
