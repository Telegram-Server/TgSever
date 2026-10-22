package postgres

import (
	"context"
	"testing"

	"telesrv/deploy"
	"telesrv/internal/store/storetest"
)

func TestChannelBannedSenderChatsPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 4)
	storetest.ChannelBannedSenderChats(t, NewChannelStore(pool), [4]int64{ids[0], ids[1], ids[2], ids[3]})
}

// TestChannelBannedSenderChatsMigrationRoundTripPostgres 校验该迁移可回滚后重新应用。
func TestChannelBannedSenderChatsMigrationRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const name = "migrations/20261029020000_channel_banned_sender_chats"
	downSQL, err := deploy.Migrations.ReadFile(name + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL, err := deploy.Migrations.ReadFile(name + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("down: %v", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.channel_banned_sender_chats') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table after down exists=%v err=%v", exists, err)
	}
	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.channel_banned_sender_chats') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("table after up exists=%v err=%v", exists, err)
	}
}
