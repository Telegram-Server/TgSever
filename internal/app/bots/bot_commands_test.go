package bots

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func scopedKey(scope domain.BotCommandScope, lang string) domain.BotCommandSetKey {
	return domain.BotCommandSetKey{Scope: scope, LangCode: lang}
}

func oneCommand(name string) []domain.BotCommand {
	return []domain.BotCommand{{Command: name, Description: name + " description"}}
}

// 按 scope 写入的桶互不覆盖；get 精确读取不回退；set 空列表删除该桶；每次变更都
// bump bot_info_version 并通知该 scope。
func TestScopedBotCommandsPersistPerScope(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	rev := &captureRevoker{}
	svc.SetRouterHooks(rev)
	owner := newOwner(t, users, "+2101")
	bot := makeBot(t, svc, owner, "Scoped Bot", "scoped_cmd_bot")
	ctx := context.Background()

	chat := domain.BotCommandScope{Type: domain.BotCommandScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 900}}
	privateScope := domain.BotCommandScope{Type: domain.BotCommandScopeUsers}
	if _, err := svc.SetBotCommands(ctx, bot.ID, oneCommand("start")); err != nil {
		t.Fatalf("set default: %v", err)
	}
	before, _, _ := users.ByID(ctx, bot.ID)
	version, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(chat, ""), oneCommand("chatonly"))
	if err != nil {
		t.Fatalf("set chat scope: %v", err)
	}
	if version <= before.BotInfoVersion {
		t.Fatalf("bot_info_version = %d, want bumped past %d", version, before.BotInfoVersion)
	}
	if rev.pushedCommandsTo != bot.ID || rev.pushedScope != chat {
		t.Fatalf("pushed = %d/%+v, want bot %d scope %+v", rev.pushedCommandsTo, rev.pushedScope, bot.ID, chat)
	}
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(privateScope, "de"), oneCommand("hallo")); err != nil {
		t.Fatalf("set users/de: %v", err)
	}

	for _, tc := range []struct {
		key  domain.BotCommandSetKey
		want string
	}{
		{scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopeDefault}, ""), "start"},
		{scopedKey(chat, ""), "chatonly"},
		{scopedKey(privateScope, "de"), "hallo"},
		{scopedKey(privateScope, ""), ""}, // 不回退到 default
		{scopedKey(chat, "de"), ""},       // 不回退到不分语言的桶
	} {
		got, found, err := svc.GetScopedBotCommands(ctx, bot.ID, tc.key)
		if err != nil {
			t.Fatalf("get %+v: %v", tc.key, err)
		}
		if tc.want == "" {
			if found || len(got) != 0 {
				t.Fatalf("get %+v = %+v found=%v, want missing", tc.key, got, found)
			}
			continue
		}
		if !found || len(got) != 1 || got[0].Command != tc.want {
			t.Fatalf("get %+v = %+v found=%v, want %q", tc.key, got, found, tc.want)
		}
	}

	// 空列表 = 删除该桶，查看者回退到更宽的 scope。
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(chat, ""), nil); err != nil {
		t.Fatalf("delete chat scope: %v", err)
	}
	if _, found, _ := svc.GetScopedBotCommands(ctx, bot.ID, scopedKey(chat, "")); found {
		t.Fatalf("chat scope still present after empty set")
	}
	resolved, err := svc.ResolveBotCommands(ctx, []int64{bot.ID}, domain.BotCommandView{Peer: chat.Peer, ViewerUserID: owner.ID})
	if err != nil || len(resolved[bot.ID]) != 1 || resolved[bot.ID][0].Command != "start" {
		t.Fatalf("resolve after delete = %+v err=%v, want default start", resolved, err)
	}
}

func TestResolveBotCommandsUsesViewerContext(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2102")
	viewer := newOwner(t, users, "+2103")
	bot := makeBot(t, svc, owner, "Resolve Bot", "resolve_cmd_bot")
	silent := makeBot(t, svc, owner, "Silent Bot", "silent_cmd_bot")
	ctx := context.Background()
	group := domain.Peer{Type: domain.PeerTypeChannel, ID: 901}

	for _, step := range []struct {
		key  domain.BotCommandSetKey
		name string
	}{
		{scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopeUsers}, ""), "private"},
		{scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopeUsers}, "zh"), "private_zh"},
		{scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopeChats}, ""), "group"},
		{scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopePeerAdmins, Peer: group}, ""), "group_admin"},
		{scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: group, UserID: viewer.ID}, ""), "just_you"},
	} {
		if _, err := svc.SetScopedBotCommands(ctx, bot.ID, step.key, oneCommand(step.name)); err != nil {
			t.Fatalf("set %+v: %v", step.key, err)
		}
	}
	for _, tc := range []struct {
		name string
		view domain.BotCommandView
		want string
	}{
		{"private", domain.BotCommandView{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, ViewerUserID: owner.ID}, "private"},
		{"private zh", domain.BotCommandView{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, ViewerUserID: owner.ID, LangCode: "zh-hans"}, "private_zh"},
		{"group member", domain.BotCommandView{Peer: group, ViewerUserID: owner.ID}, "group"},
		{"group admin", domain.BotCommandView{Peer: group, ViewerUserID: owner.ID, ViewerIsAdmin: true}, "group_admin"},
		{"group chat_member", domain.BotCommandView{Peer: group, ViewerUserID: viewer.ID, ViewerIsAdmin: true}, "just_you"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ResolveBotCommands(ctx, []int64{bot.ID, silent.ID}, tc.view)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if len(got[bot.ID]) != 1 || got[bot.ID][0].Command != tc.want {
				t.Fatalf("bot commands = %+v, want %q", got[bot.ID], tc.want)
			}
			if _, ok := got[silent.ID]; ok {
				t.Fatalf("bot without commands present in result: %+v", got[silent.ID])
			}
		})
	}
}

func TestScopedBotCommandsValidation(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2104")
	bot := makeBot(t, svc, owner, "Valid Bot", "valid_cmd_bot")
	ctx := context.Background()
	usersScope := domain.BotCommandScope{Type: domain.BotCommandScopeUsers}

	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(usersScope, "EN"), oneCommand("x")); !errors.Is(err, domain.ErrBotCommandLangCodeInvalid) {
		t.Fatalf("bad lang err = %v, want ErrBotCommandLangCodeInvalid", err)
	}
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopePeerAdmins, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}}, ""), oneCommand("x")); !errors.Is(err, domain.ErrBotCommandScopeInvalid) {
		t.Fatalf("admins scope on private chat err = %v, want ErrBotCommandScopeInvalid", err)
	}
	// 描述上限按字符计：256 个中文字符合法，257 个非法（td 用 utf8_length）。
	long := []domain.BotCommand{{Command: "zh", Description: strings.Repeat("命", domain.MaxBotCommandDescriptionLen)}}
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(usersScope, ""), long); err != nil {
		t.Fatalf("256-char description rejected: %v", err)
	}
	long[0].Description += "令"
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, scopedKey(usersScope, ""), long); !errors.Is(err, domain.ErrBotCommandInvalid) {
		t.Fatalf("257-char description err = %v, want ErrBotCommandInvalid", err)
	}
}

func TestScopedBotCommandsSetCap(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2105")
	bot := makeBot(t, svc, owner, "Cap Bot", "cap_cmd_bot")
	ctx := context.Background()
	member := func(userID int64) domain.BotCommandSetKey {
		return scopedKey(domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 902}, UserID: userID}, "")
	}
	for i := int64(1); i <= domain.MaxBotCommandSets; i++ {
		if _, err := svc.SetScopedBotCommands(ctx, bot.ID, member(i), oneCommand("x")); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, member(domain.MaxBotCommandSets+1), oneCommand("x")); !errors.Is(err, domain.ErrBotCommandSetsTooMany) {
		t.Fatalf("over-cap err = %v, want ErrBotCommandSetsTooMany", err)
	}
	// 改写已有的桶、删除桶都不受上限影响。
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, member(1), oneCommand("y")); err != nil {
		t.Fatalf("rewrite existing set at cap: %v", err)
	}
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, member(2), nil); err != nil {
		t.Fatalf("delete at cap: %v", err)
	}
	if _, err := svc.SetScopedBotCommands(ctx, bot.ID, member(domain.MaxBotCommandSets+1), oneCommand("x")); err != nil {
		t.Fatalf("set after delete freed a slot: %v", err)
	}
}
