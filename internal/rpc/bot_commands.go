package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// bots.setBotCommands / resetBotCommands / getBotCommands 按 scope + lang_code 分桶：
//   - set 传空列表 = 删除该桶（与 reset 相同），查看者回退到更宽的 scope；
//   - get 精确读取一个桶，不做回退（Bot API getMyCommands 的语义）；
//   - 查看者看到的命令由 domain.ResolveBotCommands 按 Bot API 文档的顺序选出，
//     写进 userFull.bot_info（私聊）与 channelFull.bot_info（群）。

func (r *Router) onBotsSetBotCommands(ctx context.Context, req *tg.BotsSetBotCommandsRequest) (bool, error) {
	return r.setScopedBotCommands(ctx, req.Scope, req.LangCode, domainBotCommands(req.Commands))
}

func (r *Router) onBotsResetBotCommands(ctx context.Context, req *tg.BotsResetBotCommandsRequest) (bool, error) {
	return r.setScopedBotCommands(ctx, req.Scope, req.LangCode, nil)
}

func (r *Router) setScopedBotCommands(ctx context.Context, scope tg.BotCommandScopeClass, langCode string, commands []domain.BotCommand) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	key, err := r.domainBotCommandSetKey(ctx, botID, scope, langCode)
	if err != nil {
		return false, err
	}
	if _, err := r.deps.Bots.SetScopedBotCommands(ctx, botID, key, commands); err != nil {
		return false, setBotCommandsErr(err)
	}
	r.invalidateChannelFullBotInfoCache()
	r.invalidateRPCProjectionForUser(botID)
	return true, nil
}

func (r *Router) onBotsGetBotCommands(ctx context.Context, req *tg.BotsGetBotCommandsRequest) ([]tg.BotCommand, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	key, err := r.domainBotCommandSetKey(ctx, botID, req.Scope, req.LangCode)
	if err != nil {
		return nil, err
	}
	commands, _, err := r.deps.Bots.GetScopedBotCommands(ctx, botID, key)
	if err != nil {
		return nil, setBotCommandsErr(err)
	}
	return tgBotCommands(commands), nil
}

// domainBotCommandSetKey 把 TL scope + lang_code 转成命令桶键，并校验 scope 指向的会话
// 对这个 bot 可用（td BotCommandScope::get_bot_command_scope 在客户端做同样的检查）：
//   - 私聊只能用 botCommandScopePeer；
//   - 群必须是 bot 仍在其中的超级群/基础群，broadcast 频道不能设命令；
//   - botCommandScopePeerUser 的 user 必须能解析。
func (r *Router) domainBotCommandSetKey(ctx context.Context, botID int64, scope tg.BotCommandScopeClass, langCode string) (domain.BotCommandSetKey, error) {
	if !domain.ValidBotCommandLangCode(langCode) {
		return domain.BotCommandSetKey{}, langCodeInvalidErr()
	}
	key := domain.BotCommandSetKey{LangCode: langCode}
	switch s := scope.(type) {
	case nil, *tg.BotCommandScopeDefault:
		key.Scope.Type = domain.BotCommandScopeDefault
	case *tg.BotCommandScopeUsers:
		key.Scope.Type = domain.BotCommandScopeUsers
	case *tg.BotCommandScopeChats:
		key.Scope.Type = domain.BotCommandScopeChats
	case *tg.BotCommandScopeChatAdmins:
		key.Scope.Type = domain.BotCommandScopeChatAdmins
	case *tg.BotCommandScopePeer:
		peer, err := r.botCommandScopePeer(ctx, botID, s.Peer, true)
		if err != nil {
			return domain.BotCommandSetKey{}, err
		}
		key.Scope = domain.BotCommandScope{Type: domain.BotCommandScopePeer, Peer: peer}
	case *tg.BotCommandScopePeerAdmins:
		peer, err := r.botCommandScopePeer(ctx, botID, s.Peer, false)
		if err != nil {
			return domain.BotCommandSetKey{}, err
		}
		key.Scope = domain.BotCommandScope{Type: domain.BotCommandScopePeerAdmins, Peer: peer}
	case *tg.BotCommandScopePeerUser:
		peer, err := r.botCommandScopePeer(ctx, botID, s.Peer, false)
		if err != nil {
			return domain.BotCommandSetKey{}, err
		}
		u, found, err := r.userFromInput(ctx, botID, s.UserID)
		if err != nil {
			return domain.BotCommandSetKey{}, internalErr()
		}
		if !found || u.ID == 0 {
			return domain.BotCommandSetKey{}, userIDInvalidErr()
		}
		key.Scope = domain.BotCommandScope{Type: domain.BotCommandScopePeerUser, Peer: peer, UserID: u.ID}
	default:
		return domain.BotCommandSetKey{}, peerIDInvalidErr()
	}
	return key, nil
}

func (r *Router) botCommandScopePeer(ctx context.Context, botID int64, input tg.InputPeerClass, allowPrivate bool) (domain.Peer, error) {
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, botID, input)
	if err != nil {
		return domain.Peer{}, peerIDInvalidErr()
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if !allowPrivate || peer.ID == botID || r.deps.Users == nil {
			return domain.Peer{}, peerIDInvalidErr()
		}
		if _, found, err := r.deps.Users.ByID(ctx, botID, peer.ID); err != nil {
			return domain.Peer{}, internalErr()
		} else if !found {
			return domain.Peer{}, peerIDInvalidErr()
		}
		return peer, nil
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return domain.Peer{}, peerIDInvalidErr()
		}
		view, err := r.deps.Channels.ResolveChannel(ctx, botID, peer.ID)
		if err != nil || view.Channel.ID != peer.ID {
			return domain.Peer{}, peerIDInvalidErr()
		}
		if view.Channel.Broadcast && !view.Channel.Megagroup {
			return domain.Peer{}, peerIDInvalidErr()
		}
		if view.Self.UserID != botID || view.Self.Status != domain.ChannelMemberActive {
			return domain.Peer{}, peerIDInvalidErr()
		}
		return peer, nil
	default:
		return domain.Peer{}, peerIDInvalidErr()
	}
}

// botCommandViewerLang 是当前会话查看者的命令语言（lang_code，缺省时用 system_lang_code）。
func botCommandViewerLang(ctx context.Context) string {
	return domain.BotCommandViewerLang(clientSessionMetadataFromContext(ctx).PreferredLanguage())
}

// resolvedBotCommands 选出每个 bot 在 view 会话里给查看者展示的命令。ok=false 表示
// 选取失败（调用方回落到 default 桶，不让 getFullUser / getFullChannel 因此失败）。
func (r *Router) resolvedBotCommands(ctx context.Context, botIDs []int64, view domain.BotCommandView) (map[int64][]domain.BotCommand, bool) {
	if r.deps.Bots == nil || len(botIDs) == 0 {
		return nil, false
	}
	out, err := r.deps.Bots.ResolveBotCommands(ctx, botIDs, view)
	if err != nil {
		r.log.Warn("resolve bot commands", zap.Int64s("bot_user_ids", botIDs), zap.Int64("viewer_user_id", view.ViewerUserID), zap.Error(err))
		return nil, false
	}
	return out, true
}
