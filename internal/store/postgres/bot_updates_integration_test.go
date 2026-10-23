package postgres

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

var botUpdateTestUserSeq atomic.Int64

// createBotUpdateTestUser 直接插入一个账号；bot=true 时是 bot 账号（is_bot）。
func createBotUpdateTestUser(t *testing.T, pool *pgxpool.Pool, bot bool) int64 {
	t.Helper()
	ctx := context.Background()
	seq := botUpdateTestUserSeq.Add(1)
	phone := ""
	if !bot {
		phone = fmt.Sprintf("+1556%06d%03d", time.Now().UnixNano()%1_000_000, seq%1000)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name, is_bot)
VALUES ($1, $2, $3, $4)
RETURNING id`, time.Now().UnixNano(), phone, fmt.Sprintf("qts-%d", seq), bot).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return userID
}

func TestBotUpdatesContractPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.BotUpdatesContract(t, func(t *testing.T) storetest.BotUpdatesFixture {
		return storetest.BotUpdatesFixture{
			Updates:  NewBotUpdateStore(pool),
			Channels: NewChannelStore(pool),
			NewUser:  func(t *testing.T, bot bool) int64 { return createBotUpdateTestUser(t, pool, bot) },
		}
	})
}

// 源事件与 qts 记录同事务：成员变动在写入 qts 记录之后失败回滚时，既不留下记录，
// 也不消耗 qts。用一个只拦截本测试频道的触发器让管理员日志写入失败。
func TestBotUpdatesRollBackWithSourceEventPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channels := NewChannelStore(pool)
	updates := NewBotUpdateStore(pool)
	owner, bot := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, true)
	now := int(time.Now().Unix())
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "rollback", Megagroup: true, MemberUserIDs: []int64{bot}, Date: now})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION fail_bot_qts_admin_log() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.channel_id = %d THEN
        RAISE EXCEPTION 'injected admin log failure';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER fail_bot_qts_admin_log BEFORE INSERT ON channel_admin_log_events
FOR EACH ROW EXECUTE FUNCTION fail_bot_qts_admin_log();`, channelID)); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}
	_, err = channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID: owner, ChannelID: channelID, MemberID: bot,
		AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: now,
	})
	if err == nil {
		t.Fatal("edit admin succeeded despite the injected failure")
	}
	if st, err := updates.BotUpdatesState(ctx, bot); err != nil || st != (domain.BotUpdatesState{}) {
		t.Fatalf("bot qts state after rollback = %+v, %v; want nothing allocated", st, err)
	}

	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_bot_qts_admin_log ON channel_admin_log_events`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	res, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID: owner, ChannelID: channelID, MemberID: bot,
		AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: now,
	})
	if err != nil {
		t.Fatalf("edit admin after dropping trigger: %v", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].Qts != 1 {
		t.Fatalf("bot updates after successful edit = %+v, want qts 1", res.BotUpdates)
	}
}
