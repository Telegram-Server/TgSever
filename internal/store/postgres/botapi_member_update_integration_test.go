package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// botAPIMemberUpdateFixture 建一个机器人与一个普通用户，结束时连同队列与用户一起清理。
func botAPIMemberUpdateFixture(t *testing.T, pool *pgxpool.Pool) storetest.BotAPIMemberUpdateFixture {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	bot, err := users.Create(ctx, domain.User{AccessHash: 951, Phone: "+1951" + suffix + "01", FirstName: "MemberQueueBot"})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 952, Phone: "+1952" + suffix + "01", FirstName: "MemberQueueUser"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{bot.ID, other.ID})
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO bots (bot_user_id, owner_user_id, token_secret)
VALUES ($1, $1, 'member-queue-secret')`, bot.ID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	return storetest.BotAPIMemberUpdateFixture{Store: NewBotAPIUpdateStore(pool), BotID: bot.ID, OtherUserID: other.ID}
}

func TestBotAPIChatMemberUpdatesRoundTrip(t *testing.T) {
	pool := testPool(t)
	storetest.BotAPIMemberUpdates(t, func(t *testing.T) storetest.BotAPIMemberUpdateFixture {
		return botAPIMemberUpdateFixture(t, pool)
	})
}

// 只有 Postgres 才有的部分：ListTailBotAPIUpdates 读出成员载荷，以及表约束本身。
func TestBotAPIChatMemberUpdatesTailAndConstraintsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	f := botAPIMemberUpdateFixture(t, pool)
	if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, storetest.BotAPIMemberUpdateRequest(f, domain.BotAPIUpdateMyChatMember, f.BotID)); err != nil || !created {
		t.Fatalf("enqueue my_chat_member created=%v err=%v", created, err)
	}
	tail, err := f.Store.ListTailBotAPIUpdates(ctx, f.BotID, 1, 100)
	if err != nil || len(tail) != 1 || tail[0].ChatMember == nil {
		t.Fatalf("tail = %+v err=%v", tail, err)
	}

	// 表约束也要守住：成员类行必须有载荷，消息类行不能带成员载荷。
	chat := storetest.BotAPIMemberUpdateChat(f.BotID)
	now := tail[0].Date
	if _, err := pool.Exec(ctx, `
INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, date)
VALUES ($1, 'my_chat_member', 'channel', $2, 0, $3)`, f.BotID, chat.ID, now); err == nil {
		t.Fatal("a member row without member_payload passed the table constraints")
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, date, member_payload)
VALUES ($1, 'message', 'channel', $2, 5, $3, '{}'::jsonb)`, f.BotID, chat.ID, now); err == nil {
		t.Fatal("a message row with member_payload passed the table constraints")
	}
}

func TestBotAPIJoinRequestsPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.BotAPIJoinRequests(t, func(t *testing.T) storetest.BotAPIMemberUpdateFixture {
		return botAPIMemberUpdateFixture(t, pool)
	})
}
