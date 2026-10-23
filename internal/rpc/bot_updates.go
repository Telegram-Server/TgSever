package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// BotUpdatesService 读 bot qts 序列（#145）。写入由源事件的 store 在自己的事务里完成，
// RPC 层只负责读出补差和提交后的在线推送。
type BotUpdatesService interface {
	BotUpdatesState(ctx context.Context, botUserID int64) (domain.BotUpdatesState, error)
	ListBotUpdates(ctx context.Context, botUserID int64, afterQts, limit int) ([]domain.BotUpdate, error)
}

// botQtsAccount 报告 userID 的 qts 是否来自 bot qts 序列：bot 不能用密聊，它的
// getState / getDifference 里的 qts 完全来自这条序列；普通用户仍是按设备计数的密聊 qts。
func (r *Router) botQtsAccount(ctx context.Context, userID int64) bool {
	if r.deps.BotUpdates == nil || r.deps.Users == nil || userID == 0 {
		return false
	}
	self, err := r.deps.Users.Self(ctx, userID)
	return err == nil && self.Bot
}

// botUpdateProjection 是一条 bot qts 记录的投影：channelID 是记录所属的频道（不属于频道时为 0，
// getDifference 按它批量读取频道当前投影），project 按该投影（bot 看不到时为 nil）生成 bot 看到
// 的 update，并返回需要附带的 user id 与 chat。
type botUpdateProjection struct {
	channelID int64
	project   func(view *domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass)
}

// botUpdateProjectionFor 是 bot qts 记录按类型分发的唯一位置；新的记录类型只在这里接入。
func (r *Router) botUpdateProjectionFor(update domain.BotUpdate) botUpdateProjection {
	switch {
	case update.ChannelParticipant != nil:
		return botUpdateProjection{channelID: update.ChannelParticipant.Channel.ID, project: func(view *domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
			return tgBotChannelParticipantUpdate(update, view)
		}}
	case update.ChatInviteRequester != nil:
		return botUpdateProjection{channelID: update.ChatInviteRequester.Channel.ID, project: func(view *domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
			return tgBotChatInviteRequester(update, view, r.cfg.PublicBaseURL)
		}}
	case update.MessageReaction != nil:
		return botUpdateProjection{channelID: update.MessageReaction.Channel.ID, project: func(*domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
			return tgBotMessageReactionUpdate(update)
		}}
	case update.MessageReactions != nil:
		return botUpdateProjection{channelID: update.MessageReactions.Channel.ID, project: func(*domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
			return tgBotMessageReactionsUpdate(update)
		}}
	case update.BotStopped != nil:
		return botUpdateProjection{project: func(*domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
			return tgBotStoppedUpdate(update)
		}}
	}
	return botUpdateProjection{project: func(*domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) { return nil, nil, nil }}
}

// botChannelChat 投影 bot 看到的频道：view 是频道的当前投影，bot 看得到时用它；否则（bot 已
// 不在私有频道里等）用记录里的频道快照与 bot 当时的成员身份 self。
func botChannelChat(botUserID int64, view *domain.ChannelView, snapshot domain.Channel, self domain.ChannelMember) tg.ChatClass {
	if view != nil && !view.Forbidden && view.Channel.ID == snapshot.ID {
		current := view.Self
		return tgChannelChat(botUserID, view.Channel, &current)
	}
	return tgChannelChat(botUserID, snapshot, &self)
}

// tgBotChannelParticipantUpdate 把成员变动记录投影成 updateChannelParticipant。chat 按 bot
// 自己的成员身份投影：bot 自己的变化用变化后的参与者，其他成员的变动（#146）用 Viewer。
func tgBotChannelParticipantUpdate(update domain.BotUpdate, view *domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
	p := update.ChannelParticipant
	bot := update.BotUserID
	out := &tg.UpdateChannelParticipant{
		ChannelID: p.Channel.ID,
		Date:      update.Date,
		ActorID:   p.ActorUserID,
		UserID:    p.Current.UserID,
		Qts:       update.Qts,
	}
	if out.ActorID == 0 {
		out.ActorID = bot
	}
	if p.Previous.UserID != 0 {
		out.SetPrevParticipant(tgChannelParticipantForUpdate(bot, p.Previous))
	}
	out.SetNewParticipant(tgChannelParticipantForUpdate(bot, p.Current))
	self := p.Current
	if p.Viewer.UserID == bot {
		self = p.Viewer
	}
	userIDs := []int64{out.ActorID, p.Current.UserID, p.Current.InviterUserID, p.Previous.InviterUserID}
	return out, userIDs, botChannelChat(bot, view, p.Channel, self)
}

// pushBotUpdates 在源事件提交后把 qts 记录在线推送给 bot 当前的全部会话；离线的会话
// 重连后经 updates.getDifference 按 qts 补回。
func (r *Router) pushBotUpdates(ctx context.Context, updates []domain.BotUpdate) {
	if len(updates) == 0 {
		return
	}
	cache := newViewerPeerCache(r)
	for _, update := range updates {
		tgUpdate, userIDs, chat := r.botUpdateProjectionFor(update).project(nil)
		if tgUpdate == nil {
			continue
		}
		chats := []tg.ChatClass{}
		if chat != nil {
			chats = append(chats, chat)
		}
		r.pushUserUpdates(ctx, update.BotUserID, &tg.Updates{
			Updates: []tg.UpdateClass{tgUpdate},
			Users:   tgUsersForViewer(update.BotUserID, cache.usersForIDs(ctx, update.BotUserID, uniqueRecipientIDs(userIDs))),
			Chats:   chats,
			Date:    int(r.clock.Now().Unix()),
		})
	}
}

// botQtsRecipients 是本次变动拿到 qts 记录的 bot：它们的成员变化走 qts 序列，不能再在
// 无 qts 的在线推送里收到同一个 updateChannelParticipant。
func botQtsRecipients(updates []domain.BotUpdate) map[int64]struct{} {
	if len(updates) == 0 {
		return nil
	}
	out := make(map[int64]struct{}, len(updates))
	for _, update := range updates {
		out[update.BotUserID] = struct{}{}
	}
	return out
}

// withoutChannelParticipantUpdates 去掉无 qts 的 updateChannelParticipant，保留同批的其它
// update（服务消息的 channel pts、updateChannel）。
func withoutChannelParticipantUpdates(updates *tg.Updates) *tg.Updates {
	if updates == nil {
		return nil
	}
	kept := make([]tg.UpdateClass, 0, len(updates.Updates))
	for _, update := range updates.Updates {
		if _, ok := update.(*tg.UpdateChannelParticipant); ok {
			continue
		}
		kept = append(kept, update)
	}
	out := *updates
	out.Updates = kept
	return &out
}

// botQtsDifference 读出 qts > fromQts 的一页 bot 记录并投影；返回推进后的 qts 与是否还有下一页。
//
// fromQts 早于最旧的保留记录时不返回 differenceTooLong：TDLib 收到它只重置 pts，再用同一个
// 旧 qts 请求 getDifference（td/td/telegram/UpdatesManager.cpp:2168-2176），会无限循环。改为从
// 最旧的保留记录起照常补回，超出保留期的更新视为丢弃；一条保留记录都没有、而 fromQts 落后于
// 已分配的 qts 时，qts 直接前进到当前值（调用方返回带 state 的空 updates.difference）。
func (r *Router) botQtsDifference(ctx context.Context, botUserID int64, fromQts int, cache *viewerPeerCache) (updates []tg.UpdateClass, users []tg.UserClass, chats []tg.ChatClass, newQts int, partial bool, err error) {
	rows, err := r.deps.BotUpdates.ListBotUpdates(ctx, botUserID, fromQts, domain.MaxBotUpdatesDifference+1)
	if err != nil {
		return nil, nil, nil, fromQts, false, err
	}
	newQts = fromQts
	if len(rows) == 0 {
		st, err := r.deps.BotUpdates.BotUpdatesState(ctx, botUserID)
		if err != nil {
			return nil, nil, nil, fromQts, false, err
		}
		if st.Qts > fromQts {
			newQts = st.Qts
		}
		return nil, nil, nil, newQts, false, nil
	}
	partial = len(rows) > domain.MaxBotUpdatesDifference
	if partial {
		rows = rows[:domain.MaxBotUpdatesDifference]
	}
	projections := make([]botUpdateProjection, len(rows))
	channelIDs := make([]int64, 0, len(rows))
	for i, row := range rows {
		projections[i] = r.botUpdateProjectionFor(row)
		if channelID := projections[i].channelID; channelID != 0 {
			channelIDs = append(channelIDs, channelID)
		}
	}
	views := make(map[int64]domain.ChannelView, len(channelIDs))
	if r.deps.Channels != nil && len(channelIDs) > 0 {
		if got, verr := r.deps.Channels.GetChannels(ctx, botUserID, uniqueRecipientIDs(channelIDs)); verr == nil {
			for _, view := range got {
				views[view.Channel.ID] = view
			}
		} else {
			r.log.Warn("load channels for bot qts difference", zap.Int64("bot_user_id", botUserID), zap.Error(verr))
		}
	}
	userIDs := make([]int64, 0, len(rows)*4)
	seenChats := make(map[int64]struct{}, len(channelIDs))
	for i, row := range rows {
		channelID := projections[i].channelID
		var view *domain.ChannelView
		if v, ok := views[channelID]; ok {
			view = &v
		}
		update, ids, chat := projections[i].project(view)
		newQts = row.Qts
		if update == nil {
			continue
		}
		updates = append(updates, update)
		userIDs = append(userIDs, ids...)
		if chat != nil && channelID != 0 {
			if _, ok := seenChats[channelID]; !ok {
				seenChats[channelID] = struct{}{}
				chats = append(chats, chat)
			}
		}
	}
	users = tgUsersForViewer(botUserID, cache.usersForIDs(ctx, botUserID, uniqueRecipientIDs(userIDs)))
	return updates, users, chats, newQts, partial, nil
}

// injectBotQtsDifference 把 bot qts 记录与推进后的 qts 并入差分响应。
func injectBotQtsDifference(diff tg.UpdatesDifferenceClass, updates []tg.UpdateClass, users []tg.UserClass, chats []tg.ChatClass, newQts int) tg.UpdatesDifferenceClass {
	switch v := diff.(type) {
	case *tg.UpdatesDifference:
		v.OtherUpdates = append(v.OtherUpdates, updates...)
		v.Users = append(v.Users, users...)
		v.Chats = append(v.Chats, chats...)
		v.State.Qts = newQts
	case *tg.UpdatesDifferenceSlice:
		v.OtherUpdates = append(v.OtherUpdates, updates...)
		v.Users = append(v.Users, users...)
		v.Chats = append(v.Chats, chats...)
		v.IntermediateState.Qts = newQts
	}
	return diff
}
