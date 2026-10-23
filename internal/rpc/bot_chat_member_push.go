package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// plainParticipantUpdateSkipped 报告无 qts 的 updateChannelParticipant 是否不发给 viewer（#146）。
// 接入 bot qts 序列后，bot 只经 qts 收成员变动：拿到 qts 记录的 bot（自己的变化、管理员看
// 其他成员）由 pushBotUpdates 送达；其余 bot 本就不该收到，而 TDLib 会把 qts 为 0 的这类
// update 当作错误 qts 丢弃并触发 getDifference。
func (r *Router) plainParticipantUpdateSkipped(ctx context.Context, cache *viewerPeerCache, viewerUserID int64, qtsBots map[int64]struct{}) bool {
	if _, ok := qtsBots[viewerUserID]; ok {
		return true
	}
	if r.deps.BotUpdates == nil || viewerUserID == 0 {
		return false
	}
	users := cache.usersForIDs(ctx, viewerUserID, []int64{viewerUserID})
	return len(users) == 1 && users[0].ID == viewerUserID && users[0].Bot
}

// withoutPlainParticipantUpdatesForBots 包装成员变动的按接收者投影：按 plainParticipantUpdateSkipped
// 的规则去掉发给 bot 的无 qts updateChannelParticipant，同批的其它 update 照旧。botUpdates 是本次
// 变动写下的 qts 记录（没有生成记录的路径传 nil），调用方在推送后用 pushBotUpdates 送达它们。
func (r *Router) withoutPlainParticipantUpdatesForBots(ctx context.Context, cache *viewerPeerCache, botUpdates []domain.BotUpdate, build func(viewerUserID int64) *tg.Updates) func(viewerUserID int64) *tg.Updates {
	qtsBots := botQtsRecipients(botUpdates)
	return func(viewerUserID int64) *tg.Updates {
		updates := build(viewerUserID)
		if r.plainParticipantUpdateSkipped(ctx, cache, viewerUserID, qtsBots) {
			return withoutChannelParticipantUpdates(updates)
		}
		return updates
	}
}

// channelParticipantBuilder 是单个成员变动的按接收者投影，对 bot 去掉无 qts 的
// updateChannelParticipant（见 withoutPlainParticipantUpdatesForBots）。
func (r *Router) channelParticipantBuilder(ctx context.Context, actorUserID int64, channel domain.Channel, previous, participant domain.ChannelMember, date int, botUpdates []domain.BotUpdate) func(viewerUserID int64) *tg.Updates {
	cache := newViewerPeerCache(r)
	return r.withoutPlainParticipantUpdatesForBots(ctx, cache, botUpdates, func(viewerUserID int64) *tg.Updates {
		return r.channelParticipantUpdatesWithPeerCache(ctx, viewerUserID, actorUserID, channel, previous, participant, date, cache)
	})
}
