package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestChannelLiveLocationQueuedEditCannotReviveStopPostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids := memberRulesUsers(t, pool, 1)
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], Title: "Live stop race", Megagroup: true, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	live := &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Period: 900}}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: ids[0], ChannelID: created.Channel.ID, RandomID: 910037, Date: 1700000000, Media: live})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT id FROM channels WHERE id=$1 FOR UPDATE`, created.Channel.ID); err != nil {
		t.Fatal(err)
	}

	finished := make(chan error, 1)
	go func() {
		_, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{UserID: ids[0], ChannelID: created.Channel.ID, ID: sent.Message.ID, EditDate: 1700000010, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 3, Long: 4}, Period: 900}}})
		finished <- err
	}()
	// Wait for the actual edit transaction to queue behind the channel writer.
	// The database is isolated for this test, so it cannot observe another test's locks.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("edit did not serialize with channel writer: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	stopped, err := encodeMessageMedia(&domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: domain.MessageGeoPoint{Lat: 1, Long: 2}, Period: 1, Stopped: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `UPDATE channel_messages SET media=$3 WHERE channel_id=$1 AND id=$2`, created.Channel.ID, sent.Message.ID, stopped); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, domain.ErrMessageNotModified) {
			t.Fatalf("queued edit revived stopped location: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, err := channels.GetChannelMessages(ctx, ids[0], created.Channel.ID, []int{sent.Message.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Messages) != 1 || !current.Messages[0].Media.GeoLive.Stopped || current.Messages[0].Media.GeoLive.Geo.Lat != 1 {
		t.Fatalf("stopped snapshot overwritten: %+v", current.Messages)
	}
}
