package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotAPIObjectUpdatesPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.BotAPIObjectUpdates(t, botAPIMemberUpdateFixture(t, pool))
}

func TestBotAPIObjectUpdatesRestartPostgres(t *testing.T) {
	pool := testPool(t)
	f := botAPIMemberUpdateFixture(t, pool)
	ctx := context.Background()
	object, err := domain.NewBotAPIObject("business_connection", map[string]any{"id": "connection-1", "user": map[string]any{"id": f.OtherUserID, "is_bot": false, "first_name": "Owner"}, "user_chat_id": f.OtherUserID, "date": 1700000000, "is_enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := f.Store.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{BotUserID: f.BotID, Kind: object.Kind, Date: 1700000000, Object: object})
	if err != nil || !created {
		t.Fatalf("enqueue=%+v created=%v err=%v", first, created, err)
	}
	// A new store instance has no process-local event maps or object pointers.
	reopened := NewBotAPIUpdateStore(pool)
	rows, err := reopened.ListBotAPIUpdates(ctx, f.BotID, first.ID, 100)
	if err != nil || len(rows) != 1 || rows[0].Object == nil {
		t.Fatalf("restart rows=%+v err=%v", rows, err)
	}
	body, err := rows[0].Object.Object()
	if err != nil || body["id"] != "connection-1" || body["is_enabled"] != true {
		t.Fatalf("restart snapshot=%+v err=%v", body, err)
	}
	if err := reopened.ConfirmBotAPIUpdates(ctx, f.BotID, first.ID); err != nil {
		t.Fatal(err)
	}
	confirmed, found, err := NewBotAPIUpdateStore(pool).ConfirmedBotAPIUpdateID(ctx, f.BotID)
	if err != nil || !found || confirmed != first.ID {
		t.Fatalf("restart ack=%d found=%v err=%v", confirmed, found, err)
	}
}
