package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 本文件实现 app/bots 的 rpc 回调：token revoke 后的 session 失效闭环，
// 命令变更后的 updateBotCommands 在线推送、@Stickers 发布后的 updateStickerSets
// 在线提示，以及 @ChatBot 流式草稿 transient 推送。Router 创建后经
// botsService.SetRouterHooks / SetTextDraftPusher 装配（见 cmd/telesrv/main.go）。

// maxBotCommandsPushPeers 限制单次命令变更的推送扇出：私聊取 bot 最近的这么多个用户
// dialog，群取 bot 最近的这么多个群，每个群至多推这么多个在线成员。超出的离线/长尾
// 用户靠 bot_info_version bump 与缓存失效，在下次 getFullUser / getFullChannel 时拿到新命令。
const maxBotCommandsPushPeers = 100

// maxBotCommandsPushes 是单次命令变更最多推送（并为之选取命令）的接收者总数：每人一次
// 选取查询，群的全局 scope 最坏是「群 × 在线成员」，必须封顶。
const maxBotCommandsPushes = 500

// RevokeBotSessions 撤销 bot 的全部已登录 session：删除全部 authorization 行并
// 强制断开在线连接（与 account.resetAuthorization 被踢闭环同款顺序）。
func (r *Router) RevokeBotSessions(ctx context.Context, botUserID int64) error {
	if r.deps.Auth == nil || botUserID == 0 {
		return nil
	}
	deleted, err := r.deps.Auth.ResetAuthorizations(ctx, botUserID, [8]byte{})
	if err != nil {
		return err
	}
	for _, a := range deleted {
		r.revokeAuthKeySessions(a.AuthKeyID)
		if err := r.clearAuthKeyState(ctx, a.AuthKeyID); err != nil {
			r.log.Warn("revoke bot sessions: clear auth key state",
				zap.Int64("bot_user_id", botUserID), zap.Error(err))
		}
	}
	if len(deleted) > 0 {
		r.log.Info("revoked bot sessions", zap.Int64("bot_user_id", botUserID), zap.Int("count", len(deleted)))
	}
	return nil
}

// PushBotCommandsChanged 在 scope 内的命令变更后，给受影响的在线用户推
// updateBotCommands，每人推按自己选出的那份（对齐 TDesktop/DrKLO 消费语义）：
//   - 影响私聊（default / users / 私聊 peer）：peer = bot，推给与 bot 有私聊 dialog
//     的在线用户（私聊 peer 只推那一个用户）；
//   - 影响群（default / chats / chat_admins / 群 peer / peer_admins / peer_user）：
//     peer = 群，推给 bot 所在群（群 peer 系列只推那一个群）的在线成员。
//
// updateBotCommands 无 pts/qts，不进 getDifference——离线用户由随写库一起完成的
// bot_info_version bump 与投影缓存失效兜底（下次 getFullUser / getFullChannel 重拉）。
// 推送时不知道每个会话的语言，按不分语言的桶选取；按语言分桶的 bot 在推送与下次
// 重拉之间可能短暂看到不分语言的那份。
//
// fire-and-forget：在独立 goroutine 内执行（脱离已返回的 setBotCommands RPC ctx），
// 否则 GetDialogs + 有界的 best-effort 推送会把 RPC 响应拖到拥塞超时之和、并跨用户
// 占住 BotFather 条带锁。推送是纯通知，不需保证投达；扇出见 maxBotCommandsPushPeers。
func (r *Router) PushBotCommandsChanged(ctx context.Context, botUserID int64, scope domain.BotCommandScope) {
	if r.deps.Sessions == nil || botUserID == 0 {
		return
	}
	go r.pushBotCommandsChanged(context.WithoutCancel(ctx), botUserID, scope)
}

// PushStickerSetsChanged 给单个用户在线 session 推 updateStickerSets。该 update 无
// pts，不进 getDifference；权威安装态已写 user_sticker_sets，离线端下次
// messages.getAllStickers/messages.getEmojiStickers 会重建。
func (r *Router) PushStickerSetsChanged(ctx context.Context, userID int64, kind domain.StickerSetKind) {
	if userID == 0 {
		return
	}
	r.invalidateStickerCatalog(kind)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("push sticker sets panicked", zap.Int64("user_id", userID), zap.Any("panic", rec))
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		r.pushStickerSetsUpdate(ctx, userID, kind)
	}()
}

// PushBotTextDraft 推送内置 service bot 的流式文本草稿。草稿是 TDesktop 专用的
// transient typing action，不写 message/dialog/pts/outbox；最终可恢复事实仍由随后
// 入库的普通 bot message 承担。
func (r *Router) PushBotTextDraft(ctx context.Context, botUserID, userID, randomID int64, text string) {
	if botUserID == 0 || userID == 0 || randomID == 0 || text == "" {
		return
	}
	r.pushUserMessageTransient(context.WithoutCancel(ctx), userID, "push bot text draft", &tg.UpdateShort{
		Update: &tg.UpdateUserTyping{
			UserID: botUserID,
			Action: &tg.SendMessageTextDraftAction{
				RandomID: randomID,
				Text:     tg.TextWithEntities{Text: text},
			},
		},
		Date: int(r.clock.Now().Unix()),
	})
}

func (r *Router) pushBotCommandsChanged(ctx context.Context, botUserID int64, scope domain.BotCommandScope) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("push bot commands panicked", zap.Int64("bot_user_id", botUserID), zap.Any("panic", rec))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	budget := maxBotCommandsPushes
	if botCommandScopeAffectsPrivate(scope) {
		r.pushPrivateBotCommands(ctx, botUserID, scope, &budget)
	}
	if botCommandScopeAffectsGroups(scope) {
		r.pushGroupBotCommands(ctx, botUserID, scope, &budget)
	}
}

func botCommandScopeAffectsPrivate(scope domain.BotCommandScope) bool {
	switch scope.Type {
	case domain.BotCommandScopeDefault, domain.BotCommandScopeUsers:
		return true
	case domain.BotCommandScopePeer:
		return scope.Peer.Type == domain.PeerTypeUser
	default:
		return false
	}
}

func botCommandScopeAffectsGroups(scope domain.BotCommandScope) bool {
	switch scope.Type {
	case domain.BotCommandScopeDefault, domain.BotCommandScopeChats, domain.BotCommandScopeChatAdmins,
		domain.BotCommandScopePeerAdmins, domain.BotCommandScopePeerUser:
		return true
	case domain.BotCommandScopePeer:
		return scope.Peer.Type == domain.PeerTypeChannel
	default:
		return false
	}
}

// botDialogPeers 返回 bot 最近的至多 maxBotCommandsPushPeers 个某类会话 id。
func (r *Router) botDialogPeers(ctx context.Context, botUserID int64, peerType domain.PeerType) []int64 {
	if r.deps.Dialogs == nil {
		return nil
	}
	list, err := r.deps.Dialogs.GetDialogs(ctx, botUserID, domain.DialogFilter{Limit: maxBotCommandsPushPeers})
	if err != nil {
		r.log.Warn("push bot commands: list bot dialogs", zap.Int64("bot_user_id", botUserID), zap.Error(err))
		return nil
	}
	out := make([]int64, 0, len(list.Dialogs))
	for _, dialog := range list.Dialogs {
		if dialog.Peer.Type == peerType && dialog.Peer.ID != 0 && dialog.Peer.ID != botUserID && !dialog.ChannelLeft {
			out = append(out, dialog.Peer.ID)
		}
	}
	return out
}

func (r *Router) pushPrivateBotCommands(ctx context.Context, botUserID int64, scope domain.BotCommandScope, budget *int) {
	var candidates []int64
	if scope.Type == domain.BotCommandScopePeer {
		candidates = []int64{scope.Peer.ID}
	} else {
		candidates = r.botDialogPeers(ctx, botUserID, domain.PeerTypeUser)
	}
	if len(candidates) == 0 {
		return
	}
	if provider, ok := r.deps.Sessions.(OnlineUserProvider); ok {
		candidates = provider.OnlineUserIDsForCandidates(candidates, maxBotCommandsPushPeers)
	}
	for _, userID := range candidates {
		if *budget <= 0 || ctx.Err() != nil {
			return
		}
		*budget--
		commands, ok := r.resolvedBotCommands(ctx, []int64{botUserID}, domain.BotCommandView{
			Peer:         domain.Peer{Type: domain.PeerTypeUser, ID: userID},
			ViewerUserID: userID,
		})
		if !ok {
			return
		}
		r.pushUserMessage(ctx, userID, "push bot commands", r.botCommandsUpdate(&tg.PeerUser{UserID: botUserID}, botUserID, commands[botUserID]))
	}
}

func (r *Router) pushGroupBotCommands(ctx context.Context, botUserID int64, scope domain.BotCommandScope, budget *int) {
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok || r.deps.Channels == nil {
		return
	}
	var channelIDs []int64
	if scope.Peer.Type == domain.PeerTypeChannel {
		channelIDs = []int64{scope.Peer.ID}
	} else {
		channelIDs = r.botDialogPeers(ctx, botUserID, domain.PeerTypeChannel)
	}
	for _, channelID := range channelIDs {
		if *budget <= 0 || ctx.Err() != nil {
			return
		}
		members := provider.OnlineChannelUserIDs(channelID, maxBotCommandsPushPeers)
		if scope.Type == domain.BotCommandScopePeerUser {
			members = filterInt64s(members, func(id int64) bool { return id == scope.UserID })
		}
		if len(members) == 0 {
			continue
		}
		admins := r.channelAdminSet(ctx, botUserID, channelID)
		peer := &tg.PeerChannel{ChannelID: channelID}
		for _, userID := range members {
			if userID == botUserID {
				continue
			}
			_, isAdmin := admins[userID]
			if scope.Type == domain.BotCommandScopeChatAdmins || scope.Type == domain.BotCommandScopePeerAdmins {
				if !isAdmin {
					continue // 管理员桶变化不影响普通成员
				}
			}
			if *budget <= 0 {
				return
			}
			*budget--
			commands, ok := r.resolvedBotCommands(ctx, []int64{botUserID}, domain.BotCommandView{
				Peer:          domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
				ViewerUserID:  userID,
				ViewerIsAdmin: isAdmin,
			})
			if !ok {
				return
			}
			r.pushUserMessage(ctx, userID, "push bot commands", r.botCommandsUpdate(peer, botUserID, commands[botUserID]))
		}
	}
}

// channelAdminSet 返回群里仍在任的管理员与群主（按 bot 视角读取）。
func (r *Router) channelAdminSet(ctx context.Context, botUserID, channelID int64) map[int64]struct{} {
	list, err := r.deps.Channels.GetParticipants(ctx, botUserID, channelID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}, 0, domain.MaxChannelParticipantsLimit)
	if err != nil {
		return nil
	}
	out := make(map[int64]struct{}, len(list.Participants))
	for _, member := range list.Participants {
		if domain.BotCommandViewerIsAdmin(member) {
			out[member.UserID] = struct{}{}
		}
	}
	return out
}

func (r *Router) botCommandsUpdate(peer tg.PeerClass, botUserID int64, commands []domain.BotCommand) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateBotCommands{
			Peer:     peer,
			BotID:    botUserID,
			Commands: tgBotCommands(commands),
		}},
		Date: int(r.clock.Now().Unix()),
	}
}

func filterInt64s(in []int64, keep func(int64) bool) []int64 {
	out := in[:0:0]
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}
