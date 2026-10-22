package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBusinessConnectionLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	var ids []int64
	for i := range 3 {
		user, err := users.Create(ctx, domain.User{AccessHash: int64(i + 1), Phone: fmt.Sprintf("1555%s%02d", randomSuffix(t), i), FirstName: "Business"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, user.ID)
	}
	storetest.BusinessConnections(t, NewPasswordStore(pool), ids[0], ids[1], ids[2])
}
