package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// menuButtonFixture 建一个机器人与两个普通用户，结束时清理。
func menuButtonFixture(t *testing.T, pool *pgxpool.Pool) (bot domain.User, userA, userB int64) {
	t.Helper()
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(770 + i), Phone: fmt.Sprintf("+177%s%02d", suffix, i), FirstName: "Menu"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	bot, _, err := NewBotStore(pool).CreateBotAccount(ctx, domain.User{AccessHash: 779, FirstName: "MenuBot", Username: "menu_scope_" + suffix + "_bot"}, domain.BotProfile{OwnerUserID: ids[0], TokenSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bots WHERE bot_user_id = $1`, bot.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::bigint[])`, append(ids, bot.ID))
	})
	return bot, ids[1], ids[2]
}

func TestBotUserMenuButtonsPostgres(t *testing.T) {
	pool := testPool(t)
	bot, a, b := menuButtonFixture(t, pool)
	bots := NewBotStore(pool)
	storetest.BotUserMenuButtons(t, bots, NewUserStore(pool), bot.ID, a, b, func(ctx context.Context, botID int64) error {
		_, err := bots.DeleteBotAccount(ctx, botID)
		return err
	})
}

// 专属按钮变更要跨实例失效 (该用户, 机器人) 这一对的 userFull 投影：发 dialog_light
// read-model 事件（监听器据此调用 InvalidateRPCProjectionReadModelForPeer），而不是
// bump 机器人共享的 bot_info_version 去失效所有人。
func TestBotUserMenuButtonEmitsPairReadModelEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bot, a, b := menuButtonFixture(t, pool)
	bots := NewBotStore(pool)
	pairVersion := func(owner int64) int64 {
		t.Helper()
		var v int64
		if err := pool.QueryRow(ctx, `
SELECT COALESCE(max(version), 0) FROM read_model_versions
WHERE model = 'dialog_light' AND owner_user_id = $1 AND peer_type = 'user' AND peer_id = $2`, owner, bot.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	a0, b0 := pairVersion(a), pairVersion(b)
	if err := bots.SetBotUserMenuButton(ctx, bot.ID, a, domain.BotMenuButton{Type: domain.BotMenuButtonCommands}); err != nil {
		t.Fatal(err)
	}
	a1 := pairVersion(a)
	if a1 <= a0 {
		t.Fatalf("(A, bot) read-model version %d -> %d after set, want a bump", a0, a1)
	}
	if err := bots.SetBotUserMenuButton(ctx, bot.ID, a, domain.BotMenuButton{Type: domain.BotMenuButtonDefault}); err != nil {
		t.Fatal(err)
	}
	if a2 := pairVersion(a); a2 <= a1 {
		t.Fatalf("(A, bot) read-model version %d -> %d after reset, want a bump", a1, a2)
	}
	if b1 := pairVersion(b); b1 != b0 {
		t.Fatalf("(B, bot) read-model version %d -> %d, want untouched", b0, b1)
	}
}
