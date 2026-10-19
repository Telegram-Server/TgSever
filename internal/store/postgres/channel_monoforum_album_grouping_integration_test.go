package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// TestSendMonoforumMessageKeepsAlbumGroupedIDPostgres 跑 monoforum 相册分组共享契约。
// 门控于 TELESRV_TEST_POSTGRES_DSN。
func TestSendMonoforumMessageKeepsAlbumGroupedIDPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 191, Phone: "+1781" + suffix + "41", FirstName: "MonoAlbumOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := users.Create(ctx, domain.User{AccessHash: 192, Phone: "+1781" + suffix + "42", FirstName: "MonoAlbumSub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, sub.ID})
	})
	channelIDs = storetest.MonoforumAlbumGrouping(t, NewChannelStore(pool), owner.ID, sub.ID, "Mono Album "+suffix)
}
