package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// 接线测试：选取矩阵由 domain/app 层覆盖，这里只确认 bots.* 的 scope/lang 落到对应的桶、
// 校验错误码、userFull/channelFull 按查看者选取并区分语言缓存，以及推送按 scope 定向。

func tgCommands(names ...string) []tg.BotCommand {
	out := make([]tg.BotCommand, 0, len(names))
	for _, name := range names {
		out = append(out, tg.BotCommand{Command: name, Description: name + " desc"})
	}
	return out
}

func firstCommand(commands []tg.BotCommand) string {
	if len(commands) == 0 {
		return ""
	}
	return commands[0].Command
}

func TestBotCommandScopesRPCWiring(t *testing.T) {
	f := newBotGroupFixture(t)
	botCtx := WithUserID(f.ctx, f.bot.ID)
	memberPeer := &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}
	groupPeer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	set := func(scope tg.BotCommandScopeClass, lang string, names ...string) {
		t.Helper()
		ok, err := f.router.onBotsSetBotCommands(botCtx, &tg.BotsSetBotCommandsRequest{Scope: scope, LangCode: lang, Commands: tgCommands(names...)})
		if err != nil || !ok {
			t.Fatalf("setBotCommands(%T, %q) = %v, %v", scope, lang, ok, err)
		}
	}
	get := func(scope tg.BotCommandScopeClass, lang string) string {
		t.Helper()
		got, err := f.router.onBotsGetBotCommands(botCtx, &tg.BotsGetBotCommandsRequest{Scope: scope, LangCode: lang})
		if err != nil {
			t.Fatalf("getBotCommands(%T, %q): %v", scope, lang, err)
		}
		return firstCommand(got)
	}

	set(&tg.BotCommandScopeDefault{}, "", "start")
	set(&tg.BotCommandScopeUsers{}, "", "private")
	set(&tg.BotCommandScopeUsers{}, "de", "privat")
	set(&tg.BotCommandScopePeer{Peer: memberPeer}, "", "just_ann")
	set(&tg.BotCommandScopeChats{}, "", "group")
	set(&tg.BotCommandScopePeerAdmins{Peer: groupPeer}, "", "group_admin")

	for _, tc := range []struct {
		scope tg.BotCommandScopeClass
		lang  string
		want  string
	}{
		{&tg.BotCommandScopeDefault{}, "", "start"},
		{&tg.BotCommandScopeUsers{}, "de", "privat"},
		{&tg.BotCommandScopePeer{Peer: memberPeer}, "", "just_ann"},
		{&tg.BotCommandScopePeerAdmins{Peer: groupPeer}, "", "group_admin"},
		{&tg.BotCommandScopeUsers{}, "fr", ""},             // 精确读取，不回退
		{&tg.BotCommandScopePeer{Peer: groupPeer}, "", ""}, // 精确读取，不回退
	} {
		if got := get(tc.scope, tc.lang); got != tc.want {
			t.Fatalf("getBotCommands(%T, %q) = %q, want %q", tc.scope, tc.lang, got, tc.want)
		}
	}

	t.Run("validation", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			scope tg.BotCommandScopeClass
			lang  string
			want  string
		}{
			{"uppercase lang", &tg.BotCommandScopeUsers{}, "DE", "LANG_CODE_INVALID"},
			{"admins scope in private chat", &tg.BotCommandScopePeerAdmins{Peer: memberPeer}, "", "PEER_ID_INVALID"},
			{"member scope in private chat", &tg.BotCommandScopePeerUser{Peer: memberPeer, UserID: &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}}, "", "PEER_ID_INVALID"},
			{"own chat", &tg.BotCommandScopePeer{Peer: &tg.InputPeerSelf{}}, "", "PEER_ID_INVALID"},
			{"unknown group", &tg.BotCommandScopePeer{Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID + 1000}}, "", "PEER_ID_INVALID"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := f.router.onBotsSetBotCommands(botCtx, &tg.BotsSetBotCommandsRequest{Scope: tc.scope, LangCode: tc.lang, Commands: tgCommands("x")})
				if !tgerr.Is(err, tc.want) {
					t.Fatalf("err = %v, want %s", err, tc.want)
				}
			})
		}
	})

	t.Run("userFull resolves per viewer and language", func(t *testing.T) {
		botInput := &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
		fullCommands := func(viewer int64, lang string) string {
			t.Helper()
			ctx := WithUserID(f.ctx, viewer)
			if lang != "" {
				ctx = WithClientInfo(ctx, ClientInfo{LangCode: lang})
			}
			full, err := f.router.onUsersGetFullUser(ctx, botInput)
			if err != nil {
				t.Fatalf("getFullUser by %d: %v", viewer, err)
			}
			info, ok := full.FullUser.GetBotInfo()
			if !ok {
				t.Fatalf("getFullUser by %d: missing bot_info", viewer)
			}
			commands, _ := info.GetCommands()
			return firstCommand(commands)
		}
		if got := fullCommands(f.owner.ID, ""); got != "private" {
			t.Fatalf("owner sees %q, want private", got)
		}
		if got := fullCommands(f.member.ID, ""); got != "just_ann" {
			t.Fatalf("member sees %q, want just_ann", got)
		}
		// 同一查看者换语言：缓存不能把另一种语言的结果返回出来。
		if got := fullCommands(f.owner.ID, "de-AT"); got != "privat" {
			t.Fatalf("owner (de) sees %q, want privat", got)
		}
		if got := fullCommands(f.owner.ID, ""); got != "private" {
			t.Fatalf("owner after de sees %q, want private", got)
		}
	})

	t.Run("channelFull resolves per viewer role", func(t *testing.T) {
		groupCommands := func(viewer int64) string {
			t.Helper()
			full, err := f.router.onChannelsGetFullChannel(WithUserID(f.ctx, viewer), f.inputChannel())
			if err != nil {
				t.Fatalf("getFullChannel by %d: %v", viewer, err)
			}
			channelFull, ok := full.FullChat.(*tg.ChannelFull)
			if !ok {
				t.Fatalf("full chat = %T", full.FullChat)
			}
			for _, info := range channelFull.BotInfo {
				if id, _ := info.GetUserID(); id == f.bot.ID {
					commands, _ := info.GetCommands()
					return firstCommand(commands)
				}
			}
			t.Fatalf("channelFull by %d has no bot_info for the bot", viewer)
			return ""
		}
		if got := groupCommands(f.owner.ID); got != "group_admin" {
			t.Fatalf("creator sees %q, want group_admin", got)
		}
		if got := groupCommands(f.member.ID); got != "group" {
			t.Fatalf("member sees %q, want group", got)
		}
		// 新写入的群 peer 桶要穿透 channelFull 的两层缓存。
		set(&tg.BotCommandScopePeer{Peer: groupPeer}, "", "this_group")
		if got := groupCommands(f.member.ID); got != "this_group" {
			t.Fatalf("member after group set sees %q, want this_group", got)
		}
		// reset 删除该桶，回退到 chats。
		if ok, err := f.router.onBotsResetBotCommands(botCtx, &tg.BotsResetBotCommandsRequest{Scope: &tg.BotCommandScopePeer{Peer: groupPeer}}); err != nil || !ok {
			t.Fatalf("reset group scope = %v, %v", ok, err)
		}
		if got := groupCommands(f.member.ID); got != "group" {
			t.Fatalf("member after reset sees %q, want group", got)
		}
	})
}

// botDialogsStub 只回答 bot 的 dialog 列表（推送用它找 bot 所在的群与私聊）。
type botDialogsStub struct {
	DialogsService
	peers []domain.Peer
}

func (s botDialogsStub) GetDialogs(context.Context, int64, domain.DialogFilter) (domain.DialogList, error) {
	list := domain.DialogList{}
	for _, peer := range s.peers {
		list.Dialogs = append(list.Dialogs, domain.Dialog{Peer: peer})
	}
	return list, nil
}

func TestBotCommandsPushTargetsScope(t *testing.T) {
	f := newBotGroupFixture(t)
	f.router.deps.Dialogs = botDialogsStub{peers: []domain.Peer{
		{Type: domain.PeerTypeChannel, ID: f.channel.ID},
		{Type: domain.PeerTypeUser, ID: f.owner.ID},
	}}
	f.sessions.mu.Lock()
	f.sessions.onlineUserIDs = []int64{f.owner.ID, f.member.ID}
	f.sessions.channelViewers = map[int64][]int64{f.channel.ID: {f.owner.ID, f.member.ID, f.bot.ID}}
	f.sessions.mu.Unlock()
	reset := func() {
		f.sessions.mu.Lock()
		f.sessions.pushUserIDs = nil
		f.sessions.userMessage = nil
		f.sessions.mu.Unlock()
	}
	pushed := func() ([]int64, *tg.UpdateBotCommands) {
		f.sessions.mu.Lock()
		defer f.sessions.mu.Unlock()
		ids := append([]int64(nil), f.sessions.pushUserIDs...)
		updates, _ := f.sessions.userMessage.(*tg.Updates)
		if updates == nil || len(updates.Updates) != 1 {
			return ids, nil
		}
		update, _ := updates.Updates[0].(*tg.UpdateBotCommands)
		return ids, update
	}
	ctx := WithUserID(f.ctx, f.bot.ID)
	if _, err := f.bots.SetScopedBotCommands(ctx, f.bot.ID, domain.BotCommandSetKey{Scope: domain.BotCommandScope{Type: domain.BotCommandScopeChatAdmins}}, []domain.BotCommand{{Command: "ban", Description: "ban someone"}}); err != nil {
		t.Fatal(err)
	}

	// 群的管理员桶：只推群里在线的管理员（群主），peer 是群，内容是他选出的那份。
	reset()
	f.router.pushBotCommandsChanged(f.ctx, f.bot.ID, domain.BotCommandScope{Type: domain.BotCommandScopeChatAdmins})
	ids, update := pushed()
	if len(ids) != 1 || ids[0] != f.owner.ID {
		t.Fatalf("chat_admins push went to %v, want only the creator %d", ids, f.owner.ID)
	}
	if update == nil || update.BotID != f.bot.ID || firstCommand(update.Commands) != "ban" {
		t.Fatalf("chat_admins update = %+v, want bot %d with ban", update, f.bot.ID)
	}
	if peer, ok := update.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != f.channel.ID {
		t.Fatalf("chat_admins update peer = %+v, want the group", update.Peer)
	}

	// 私聊 peer 桶：只推那一个用户，peer 是 bot。
	reset()
	f.router.pushBotCommandsChanged(f.ctx, f.bot.ID, domain.BotCommandScope{Type: domain.BotCommandScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}})
	ids, update = pushed()
	if len(ids) != 1 || ids[0] != f.member.ID {
		t.Fatalf("private peer push went to %v, want only member %d", ids, f.member.ID)
	}
	if peer, ok := update.Peer.(*tg.PeerUser); !ok || peer.UserID != f.bot.ID {
		t.Fatalf("private peer update peer = %+v, want the bot", update.Peer)
	}

	// 群成员桶：只推那个成员。
	reset()
	f.router.pushBotCommandsChanged(f.ctx, f.bot.ID, domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}, UserID: f.member.ID})
	if ids, _ = pushed(); len(ids) != 1 || ids[0] != f.member.ID {
		t.Fatalf("peer_user push went to %v, want only member %d", ids, f.member.ID)
	}
}
