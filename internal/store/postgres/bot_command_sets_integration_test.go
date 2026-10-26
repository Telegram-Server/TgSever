package postgres

import (
	"context"
	"errors"
	"sort"
	"testing"

	"telesrv/internal/domain"
)

func TestBotCommandSetsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bot, viewer, other := menuButtonFixture(t, pool)
	bots := NewBotStore(pool)
	users := NewUserStore(pool)
	group := domain.Peer{Type: domain.PeerTypeChannel, ID: 880001}
	key := func(scope domain.BotCommandScope, lang string) domain.BotCommandSetKey {
		return domain.BotCommandSetKey{Scope: scope, LangCode: lang}
	}
	command := func(name string) []domain.BotCommand {
		return []domain.BotCommand{{Command: name, Description: name + " desc", Ephemeral: name == "eph"}}
	}

	sets := []struct {
		key  domain.BotCommandSetKey
		name string
	}{
		{key(domain.BotCommandScope{Type: domain.BotCommandScopeDefault}, "zh"), "default_zh"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopeUsers}, ""), "users"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopeChats}, "de"), "chats_de"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: viewer}}, ""), "private_peer"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopePeer, Peer: group}, ""), "group_peer"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopePeerAdmins, Peer: group}, ""), "group_admins"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: group, UserID: viewer}, ""), "eph"},
		{key(domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: group, UserID: other}, ""), "other_member"},
	}
	before, _, err := users.ByID(ctx, bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	for _, s := range sets {
		if version, err = bots.SetBotCommandSet(ctx, bot.ID, s.key, command(s.name)); err != nil {
			t.Fatalf("set %+v: %v", s.key, err)
		}
	}
	if version != before.BotInfoVersion+len(sets) {
		t.Fatalf("bot_info_version = %d, want %d (one bump per write)", version, before.BotInfoVersion+len(sets))
	}

	// 精确读取：每个桶读回自己的命令，ephemeral 标志随 jsonb 往返。
	for _, s := range sets {
		got, found, err := bots.GetBotCommandSet(ctx, bot.ID, s.key)
		if err != nil || !found || len(got) != 1 || got[0].Command != s.name || got[0].Ephemeral != (s.name == "eph") {
			t.Fatalf("get %+v = %+v found=%v err=%v, want %q", s.key, got, found, err, s.name)
		}
	}
	if _, found, err := bots.GetBotCommandSet(ctx, bot.ID, key(domain.BotCommandScope{Type: domain.BotCommandScopeUsers}, "fr")); err != nil || found {
		t.Fatalf("missing set found=%v err=%v, want not found", found, err)
	}

	// 按会话列出：全局桶 + 绑在这个会话上的桶，peer_user 只取查看者自己的。
	listNames := func(peer domain.Peer, viewerUserID int64) []string {
		t.Helper()
		listed, err := bots.ListBotCommandSetsForPeer(ctx, []int64{bot.ID}, peer, viewerUserID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		names := make([]string, 0, len(listed))
		for _, s := range listed {
			if s.BotUserID != bot.ID {
				t.Fatalf("listed set of bot %d", s.BotUserID)
			}
			names = append(names, s.Commands[0].Command)
		}
		sort.Strings(names)
		return names
	}
	assertNames := func(got []string, want ...string) {
		t.Helper()
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("listed %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("listed %v, want %v", got, want)
			}
		}
	}
	assertNames(listNames(group, viewer), "default_zh", "users", "chats_de", "group_peer", "group_admins", "eph")
	assertNames(listNames(domain.Peer{Type: domain.PeerTypeUser, ID: viewer}, viewer), "default_zh", "users", "chats_de", "private_peer")
	assertNames(listNames(domain.Peer{Type: domain.PeerTypeUser, ID: other}, other), "default_zh", "users", "chats_de")

	// default + 空 lang 走 bots.commands。
	defaultKey := key(domain.BotCommandScope{Type: domain.BotCommandScopeDefault}, "")
	if _, err := bots.SetBotCommandSet(ctx, bot.ID, defaultKey, command("start")); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if profile, _, err := bots.GetBot(ctx, bot.ID); err != nil || len(profile.Commands) != 1 || profile.Commands[0].Command != "start" {
		t.Fatalf("bots.commands = %+v err=%v, want start", profile.Commands, err)
	}

	// 空列表删除；删除不存在的桶也成功。
	if _, err := bots.SetBotCommandSet(ctx, bot.ID, sets[4].key, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := bots.SetBotCommandSet(ctx, bot.ID, sets[4].key, nil); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, found, _ := bots.GetBotCommandSet(ctx, bot.ID, sets[4].key); found {
		t.Fatal("deleted set still present")
	}

	// 上限：再插到 MaxBotCommandSets 个，下一个新桶被拒，改写已有桶不受影响。
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_command_sets WHERE bot_user_id = $1`, bot.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO bot_command_sets (bot_user_id, scope, peer_type, peer_id, user_id, commands)
SELECT $1, 'peer_user', 'channel', 880002, g, '[{"command":"x","description":"x"}]'::jsonb
FROM generate_series(1, $2::int) AS g`, bot.ID, domain.MaxBotCommandSets-count); err != nil {
		t.Fatalf("fill sets: %v", err)
	}
	overflow := key(domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 880002}, UserID: 999999}, "")
	if _, err := bots.SetBotCommandSet(ctx, bot.ID, overflow, command("x")); !errors.Is(err, domain.ErrBotCommandSetsTooMany) {
		t.Fatalf("over-cap err = %v, want ErrBotCommandSetsTooMany", err)
	}
	if _, err := bots.SetBotCommandSet(ctx, bot.ID, sets[1].key, command("users2")); err != nil {
		t.Fatalf("rewrite at cap: %v", err)
	}

	// 删除 bot 时级联清掉它的桶。
	if _, err := pool.Exec(ctx, `DELETE FROM bots WHERE bot_user_id = $1`, bot.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_command_sets WHERE bot_user_id = $1`, bot.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("sets after bot delete = %d err=%v, want 0", count, err)
	}
}
