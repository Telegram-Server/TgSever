package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// TestSendMonoforumMessageKeepsAlbumGroupedIDPostgres 跑 monoforum 相册分组共享契约。
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
	storetest.MonoforumAlbumGrouping(t, NewChannelStore(pool), owner.ID, sub.ID, "Mono Album "+suffix)
}
