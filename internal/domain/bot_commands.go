package domain

import (
	"errors"
	"strings"
)

// BotCommandScopeType 是 bot 命令的作用域（Bot API BotCommandScope / MTProto
// botCommandScope*）。peer 系列绑在 bot 视角的一个会话上。
type BotCommandScopeType string

const (
	// BotCommandScopeDefault 对所有会话生效（botCommandScopeDefault）。
	BotCommandScopeDefault BotCommandScopeType = "default"
	// BotCommandScopeUsers 对所有私聊生效（botCommandScopeUsers / all_private_chats）。
	BotCommandScopeUsers BotCommandScopeType = "users"
	// BotCommandScopeChats 对所有群生效（botCommandScopeChats / all_group_chats）。
	BotCommandScopeChats BotCommandScopeType = "chats"
	// BotCommandScopeChatAdmins 对所有群的管理员生效（botCommandScopeChatAdmins）。
	BotCommandScopeChatAdmins BotCommandScopeType = "chat_admins"
	// BotCommandScopePeer 对一个会话生效（botCommandScopePeer / chat）：私聊或群。
	BotCommandScopePeer BotCommandScopeType = "peer"
	// BotCommandScopePeerAdmins 对一个群的管理员生效（botCommandScopePeerAdmins）。
	BotCommandScopePeerAdmins BotCommandScopeType = "peer_admins"
	// BotCommandScopePeerUser 对一个群里的一个成员生效（botCommandScopePeerUser / chat_member）。
	BotCommandScopePeerUser BotCommandScopeType = "peer_user"
)

// MaxBotCommandSets 是一个 bot 除 default+空 lang 之外最多保存的命令桶数。peer_user
// 可以按「群 × 成员」无限增长，必须有上限。
const MaxBotCommandSets = 1000

var (
	// ErrBotCommandLangCodeInvalid 表示 lang_code 不是空串或 2 位小写字母（LANG_CODE_INVALID）。
	ErrBotCommandLangCodeInvalid = errors.New("bot command lang code invalid")
	// ErrBotCommandScopeInvalid 表示 scope 指向的会话不可用（PEER_ID_INVALID）：私聊用了
	// 群专属 scope、broadcast 频道、bot 不在群里等。
	ErrBotCommandScopeInvalid = errors.New("bot command scope invalid")
	// ErrBotCommandSetsTooMany 表示 bot 的命令桶数已达 MaxBotCommandSets。
	ErrBotCommandSetsTooMany = errors.New("bot command sets too many")
)

// BotCommandScope 是一个作用域。Peer 只对 peer 系列有意义；UserID 只对 peer_user 有意义。
type BotCommandScope struct {
	Type   BotCommandScopeType
	Peer   Peer
	UserID int64
}

// BotCommandSetKey 定位一个命令桶：作用域 + 语言（空 = 对该作用域的所有语言生效）。
type BotCommandSetKey struct {
	Scope    BotCommandScope
	LangCode string
}

// IsDefault 报告这一桶是否是存在 bots.commands 的 default + 空 lang 桶。
func (k BotCommandSetKey) IsDefault() bool {
	return k.Scope.Type == BotCommandScopeDefault && k.LangCode == ""
}

// BotCommandSet 是一个已保存的非空命令桶。
type BotCommandSet struct {
	BotUserID int64
	Key       BotCommandSetKey
	Commands  []BotCommand
}

// Valid 校验作用域的形状（不校验会话是否存在、bot 是否在群里）。
func (s BotCommandScope) Valid() bool {
	switch s.Type {
	case BotCommandScopeDefault, BotCommandScopeUsers, BotCommandScopeChats, BotCommandScopeChatAdmins:
		return s.Peer == (Peer{}) && s.UserID == 0
	case BotCommandScopePeer:
		return (s.Peer.Type == PeerTypeUser || s.Peer.Type == PeerTypeChannel) && s.Peer.ID > 0 && s.UserID == 0
	case BotCommandScopePeerAdmins:
		return s.Peer.Type == PeerTypeChannel && s.Peer.ID > 0 && s.UserID == 0
	case BotCommandScopePeerUser:
		return s.Peer.Type == PeerTypeChannel && s.Peer.ID > 0 && s.UserID > 0
	default:
		return false
	}
}

// ValidBotCommandLangCode 对齐 td validate_bot_language_code：空串或 2 位小写字母。
func ValidBotCommandLangCode(code string) bool {
	return code == "" || (len(code) == 2 && isLowerASCII(code[0]) && isLowerASCII(code[1]))
}

// BotCommandViewerLang 把查看者客户端的语言（initConnection lang_code，如 "zh-hans"、
// "pt-BR"、"en"）规整成命令桶的 2 位语言码；无法规整时返回空串（只匹配不分语言的桶）。
func BotCommandViewerLang(langCode string) string {
	code := strings.ToLower(strings.TrimSpace(langCode))
	if len(code) < 2 || !isLowerASCII(code[0]) || !isLowerASCII(code[1]) {
		return ""
	}
	if len(code) > 2 && code[2] != '-' && code[2] != '_' {
		return ""
	}
	return code[:2]
}

// BotCommandViewerIsAdmin 报告群成员是否按「管理员」查找 chat_administrators /
// all_chat_administrators 桶：仍在群里的管理员或群主。
func BotCommandViewerIsAdmin(member ChannelMember) bool {
	return member.Status == ChannelMemberActive && (member.Role == ChannelRoleAdmin || member.Role == ChannelRoleCreator)
}

func isLowerASCII(b byte) bool { return b >= 'a' && b <= 'z' }

// BotCommandView 描述查看者在哪个会话里看 bot 的命令。
//   - 私聊：Peer 是 bot 视角的会话，即 {user, ViewerUserID}；
//   - 群：Peer 是 {channel, 频道 id}，ViewerIsAdmin 是查看者在群里是否为管理员/创建者。
type BotCommandView struct {
	Peer          Peer
	ViewerUserID  int64
	ViewerIsAdmin bool
	LangCode      string
}

// BotCommandCandidates 返回该查看者依次查找的桶，第一个存在的桶即为结果
// （Bot API 文档 "Determining list of commands" 的顺序；每级先查带语言的，再查不带的）。
func BotCommandCandidates(view BotCommandView) []BotCommandSetKey {
	var scopes []BotCommandScope
	switch view.Peer.Type {
	case PeerTypeUser:
		scopes = []BotCommandScope{
			{Type: BotCommandScopePeer, Peer: view.Peer},
			{Type: BotCommandScopeUsers},
			{Type: BotCommandScopeDefault},
		}
	case PeerTypeChannel:
		scopes = append(scopes, BotCommandScope{Type: BotCommandScopePeerUser, Peer: view.Peer, UserID: view.ViewerUserID})
		if view.ViewerIsAdmin {
			scopes = append(scopes, BotCommandScope{Type: BotCommandScopePeerAdmins, Peer: view.Peer})
		}
		scopes = append(scopes, BotCommandScope{Type: BotCommandScopePeer, Peer: view.Peer})
		if view.ViewerIsAdmin {
			scopes = append(scopes, BotCommandScope{Type: BotCommandScopeChatAdmins})
		}
		scopes = append(scopes, BotCommandScope{Type: BotCommandScopeChats}, BotCommandScope{Type: BotCommandScopeDefault})
	default:
		scopes = []BotCommandScope{{Type: BotCommandScopeDefault}}
	}
	lang := BotCommandViewerLang(view.LangCode)
	out := make([]BotCommandSetKey, 0, 2*len(scopes))
	for _, scope := range scopes {
		if lang != "" {
			out = append(out, BotCommandSetKey{Scope: scope, LangCode: lang})
		}
		out = append(out, BotCommandSetKey{Scope: scope})
	}
	return out
}

// ResolveBotCommands 从一个 bot 的已保存桶里选出查看者看到的命令：按
// BotCommandCandidates 的顺序取第一个非空桶；都没有时返回 nil。defaultCommands 是
// bots.commands 里的 default + 空 lang 桶。
func ResolveBotCommands(defaultCommands []BotCommand, sets []BotCommandSet, view BotCommandView) []BotCommand {
	byKey := make(map[BotCommandSetKey][]BotCommand, len(sets)+1)
	for _, set := range sets {
		if len(set.Commands) > 0 {
			byKey[set.Key] = set.Commands
		}
	}
	if len(defaultCommands) > 0 {
		byKey[BotCommandSetKey{Scope: BotCommandScope{Type: BotCommandScopeDefault}}] = defaultCommands
	}
	for _, key := range BotCommandCandidates(view) {
		if commands, ok := byKey[key]; ok {
			return append([]BotCommand(nil), commands...)
		}
	}
	return nil
}
