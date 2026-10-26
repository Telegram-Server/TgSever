package domain

import (
	"testing"
)

func cmds(names ...string) []BotCommand {
	out := make([]BotCommand, 0, len(names))
	for _, name := range names {
		out = append(out, BotCommand{Command: name, Description: name})
	}
	return out
}

func commandNames(in []BotCommand) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, c.Command)
	}
	return out
}

// 选取顺序对照 Bot API 文档 "Determining list of commands"：第一个存在的桶即结果，
// 每级先查带语言的、再查不带语言的；管理员桶只对管理员生效。
func TestResolveBotCommands(t *testing.T) {
	const viewer, other = int64(10), int64(11)
	private := Peer{Type: PeerTypeUser, ID: viewer}
	group := Peer{Type: PeerTypeChannel, ID: 500}
	otherGroup := Peer{Type: PeerTypeChannel, ID: 501}
	set := func(scope BotCommandScope, lang string, names ...string) BotCommandSet {
		return BotCommandSet{BotUserID: 1, Key: BotCommandSetKey{Scope: scope, LangCode: lang}, Commands: cmds(names...)}
	}
	all := []BotCommandSet{
		set(BotCommandScope{Type: BotCommandScopeDefault}, "zh", "default_zh"),
		set(BotCommandScope{Type: BotCommandScopeUsers}, "", "users"),
		set(BotCommandScope{Type: BotCommandScopeUsers}, "de", "users_de"),
		set(BotCommandScope{Type: BotCommandScopeChats}, "", "chats"),
		set(BotCommandScope{Type: BotCommandScopeChatAdmins}, "", "chat_admins"),
		set(BotCommandScope{Type: BotCommandScopePeer, Peer: group}, "", "peer"),
		set(BotCommandScope{Type: BotCommandScopePeerAdmins, Peer: group}, "", "peer_admins"),
		set(BotCommandScope{Type: BotCommandScopePeerUser, Peer: group, UserID: viewer}, "", "peer_user"),
		set(BotCommandScope{Type: BotCommandScopePeer, Peer: private}, "fr", "private_peer_fr"),
	}
	defaults := cmds("default")
	cases := []struct {
		name     string
		defaults []BotCommand
		sets     []BotCommandSet
		view     BotCommandView
		want     []string
	}{
		{"private: users beats default", defaults, all, BotCommandView{Peer: private, ViewerUserID: viewer}, []string{"users"}},
		{"private: lang-specific users beats plain users", defaults, all, BotCommandView{Peer: private, ViewerUserID: viewer, LangCode: "de-AT"}, []string{"users_de"}},
		{"private: peer+lang beats everything", defaults, all, BotCommandView{Peer: private, ViewerUserID: viewer, LangCode: "fr"}, []string{"private_peer_fr"}},
		{"private: peer+lang does not match another language", defaults, all, BotCommandView{Peer: private, ViewerUserID: viewer, LangCode: "es"}, []string{"users"}},
		{"private: falls back to default+lang", defaults, []BotCommandSet{all[0]}, BotCommandView{Peer: private, ViewerUserID: viewer, LangCode: "zh-hans"}, []string{"default_zh"}},
		{"private: falls back to default", defaults, []BotCommandSet{all[0]}, BotCommandView{Peer: private, ViewerUserID: viewer}, []string{"default"}},
		{"private: group scopes never apply", defaults, []BotCommandSet{all[3], all[4]}, BotCommandView{Peer: private, ViewerUserID: viewer}, []string{"default"}},
		{"group: peer_user beats admin scopes", defaults, all, BotCommandView{Peer: group, ViewerUserID: viewer, ViewerIsAdmin: true}, []string{"peer_user"}},
		{"group admin: peer_admins beats peer", defaults, all, BotCommandView{Peer: group, ViewerUserID: other, ViewerIsAdmin: true}, []string{"peer_admins"}},
		{"group member: peer_admins skipped", defaults, all, BotCommandView{Peer: group, ViewerUserID: other}, []string{"peer"}},
		{"group admin elsewhere: chat_admins beats chats", defaults, all, BotCommandView{Peer: otherGroup, ViewerUserID: other, ViewerIsAdmin: true}, []string{"chat_admins"}},
		{"group member elsewhere: chats", defaults, all, BotCommandView{Peer: otherGroup, ViewerUserID: other}, []string{"chats"}},
		{"group: users scope never applies", defaults, []BotCommandSet{all[1]}, BotCommandView{Peer: otherGroup, ViewerUserID: other}, []string{"default"}},
		{"group: peer_user of another member not used", defaults, []BotCommandSet{all[7]}, BotCommandView{Peer: group, ViewerUserID: other}, []string{"default"}},
		{"nothing set", nil, nil, BotCommandView{Peer: private, ViewerUserID: viewer}, nil},
		{"empty set is ignored", defaults, []BotCommandSet{set(BotCommandScope{Type: BotCommandScopeUsers}, "")}, BotCommandView{Peer: private, ViewerUserID: viewer}, []string{"default"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandNames(ResolveBotCommands(tc.defaults, tc.sets, tc.view))
			if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
				t.Fatalf("ResolveBotCommands = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBotCommandViewerLang(t *testing.T) {
	for in, want := range map[string]string{
		"en": "en", "zh-hans": "zh", "pt-BR": "pt", "pt_br": "pt", " DE ": "de",
		"": "", "e": "", "eng": "", "1a": "", "zh-": "zh",
	} {
		if got := BotCommandViewerLang(in); got != want {
			t.Fatalf("BotCommandViewerLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBotCommandSetKeyValidation(t *testing.T) {
	user := Peer{Type: PeerTypeUser, ID: 7}
	channel := Peer{Type: PeerTypeChannel, ID: 8}
	for _, tc := range []struct {
		scope BotCommandScope
		want  bool
	}{
		{BotCommandScope{Type: BotCommandScopeDefault}, true},
		{BotCommandScope{Type: BotCommandScopeChats}, true},
		{BotCommandScope{Type: BotCommandScopeUsers, Peer: user}, false},
		{BotCommandScope{Type: BotCommandScopePeer, Peer: user}, true},
		{BotCommandScope{Type: BotCommandScopePeer, Peer: channel}, true},
		{BotCommandScope{Type: BotCommandScopePeer}, false},
		{BotCommandScope{Type: BotCommandScopePeerAdmins, Peer: user}, false},
		{BotCommandScope{Type: BotCommandScopePeerAdmins, Peer: channel}, true},
		{BotCommandScope{Type: BotCommandScopePeerUser, Peer: channel}, false},
		{BotCommandScope{Type: BotCommandScopePeerUser, Peer: channel, UserID: 9}, true},
		{BotCommandScope{Type: BotCommandScopePeerUser, Peer: user, UserID: 9}, false},
		{BotCommandScope{Type: "bogus"}, false},
	} {
		if got := tc.scope.Valid(); got != tc.want {
			t.Fatalf("%+v.Valid() = %v, want %v", tc.scope, got, tc.want)
		}
	}
	for code, want := range map[string]bool{"": true, "en": true, "EN": false, "eng": false, "e1": false} {
		if got := ValidBotCommandLangCode(code); got != want {
			t.Fatalf("ValidBotCommandLangCode(%q) = %v, want %v", code, got, want)
		}
	}
}
