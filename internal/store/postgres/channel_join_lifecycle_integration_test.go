package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelJoinRequestLifecyclePostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	s := NewChannelStore(pool)
	storetest.ChannelJoinRequestLifecycle(t, s, [4]int64{ids[0], ids[1], ids[2], ids[3]}, func(channelID int64, invite domain.ChannelInvite, userID int64, date int) {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `INSERT INTO channel_invite_importers(channel_id, invite_id, user_id, date, requested) VALUES ($1,$2,$3,$4,true)`, channelID, invite.InviteID, userID, date); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE channel_invites SET requested_count = requested_count + 1 WHERE channel_id=$1 AND invite_id=$2`, channelID, invite.InviteID); err != nil {
			t.Fatal(err)
		}
	})
}
