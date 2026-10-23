package postgres

import (
	"context"
	"fmt"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 拉黑/解除拉黑 bot 的 updateBotStopped 与拉黑名单同事务写入（#148）：名单写入失败回滚时
// 不留记录、不消耗 qts；成功后记录能从 bot qts 序列读回（离线 getDifference 的来源）；
// 拉黑普通用户不写任何 bot 记录。
func TestBlocklistBotStoppedSharesTransactionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	contacts := NewContactStore(pool)
	updates := NewBotUpdateStore(pool)
	user, bot, human := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, true), createBotUpdateTestUser(t, pool, false)
	const date = 1700000100
	mutate := func(kind store.BlocklistMutationKind, peer int64) (store.BlocklistMutationSnapshot, error) {
		return contacts.MutateBlocklist(ctx, store.BlocklistMutation{Kind: kind, OwnerUserID: user, PeerIDs: []int64{peer}, Date: date}, store.BlocklistDeliveryEffects)
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION fail_bot_stopped_block() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.owner_user_id = %d THEN
        RAISE EXCEPTION 'injected contact block failure';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER fail_bot_stopped_block BEFORE INSERT ON contact_blocks
FOR EACH ROW EXECUTE FUNCTION fail_bot_stopped_block();`, user)); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}
	if _, err := mutate(store.BlocklistBlock, bot); err == nil {
		t.Fatal("block succeeded despite the injected failure")
	}
	if st, err := updates.BotUpdatesState(ctx, bot); err != nil || st != (domain.BotUpdatesState{}) {
		t.Fatalf("bot qts state after rollback = %+v, %v; want nothing allocated", st, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_bot_stopped_block ON contact_blocks`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	blocked, err := mutate(store.BlocklistBlock, bot)
	if err != nil {
		t.Fatalf("block bot: %v", err)
	}
	if len(blocked.BotUpdates) != 1 || blocked.BotUpdates[0].Qts != 1 {
		t.Fatalf("block bot returned %+v, want the qts=1 record", blocked.BotUpdates)
	}
	if again, err := mutate(store.BlocklistBlock, bot); err != nil || len(again.BotUpdates) != 0 {
		t.Fatalf("repeated block returned %+v, %v", again.BotUpdates, err)
	}
	if _, err := mutate(store.BlocklistBlock, human); err != nil {
		t.Fatalf("block human: %v", err)
	}
	if _, err := mutate(store.BlocklistUnblock, bot); err != nil {
		t.Fatalf("unblock bot: %v", err)
	}

	rows, err := updates.ListBotUpdates(ctx, bot, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("bot qts records = %+v, want block and unblock", rows)
	}
	for i, stopped := range []bool{true, false} {
		row := rows[i]
		if row.Qts != i+1 || row.Date != date || row.BotStopped == nil || *row.BotStopped != (domain.BotStoppedUpdate{UserID: user, Stopped: stopped}) {
			t.Fatalf("record %d = %+v (bot stopped %+v), want user %d stopped=%v", i, row, row.BotStopped, user, stopped)
		}
	}
	if st, err := updates.BotUpdatesState(ctx, human); err != nil || st.Qts != 0 {
		t.Fatalf("human qts state = %+v, %v; want none", st, err)
	}
}
