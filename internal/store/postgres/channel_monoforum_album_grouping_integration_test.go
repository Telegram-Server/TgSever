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
	// 契约中途 Fatal 也要清理：广播频道与 monoforum 都由 owner 创建，按创建者删除。
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE creator_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, sub.ID})
	})
	storetest.MonoforumAlbumGrouping(t, NewChannelStore(pool), owner.ID, sub.ID, "Mono Album "+suffix)
}
