package rpc

import "context"

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
