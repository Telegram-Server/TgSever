package memory

import (
	"sort"

	"telesrv/internal/domain"
)

// withAdminBotAudience 把成员变动的 qts 记录候选复制给群里每个管理员 bot（#146）。
// 调用方不持有 s.mu。
func (s *ChannelStore) withAdminBotAudience(channelID int64, changes []domain.BotUpdate) []domain.BotUpdate {
	if len(changes) == 0 || s.botUpdates == nil {
		return changes
	}
	s.mu.RLock()
	admins := s.adminBotMembersLocked(channelID)
	s.mu.RUnlock()
	return domain.WithChannelAdminBotAudience(changes, admins)
}

// adminBotMembersLocked 返回群里的管理员 bot 成员记录（按 user id 排序）；调用方持有 s.mu。
func (s *ChannelStore) adminBotMembersLocked(channelID int64) []domain.ChannelMember {
	var out []domain.ChannelMember
	for _, member := range s.members[channelID] {
		if domain.IsChannelAdminBotAudience(member) && s.botUpdates.isBot(member.UserID) {
			out = append(out, member)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

// appendInviteImporterBotUpdatesLocked 在批准入群（邀请链接、入群申请）的临界区里写入
// 新成员本人（是 bot 时）与管理员 bot 的 qts 记录；调用方持有 s.mu。
func (s *ChannelStore) appendInviteImporterBotUpdatesLocked(res domain.CreateChannelResult, actorUserID int64, date int) ([]domain.BotUpdate, error) {
	if s.botUpdates == nil {
		return nil, nil
	}
	changes := domain.BotChannelParticipantChanges(res, actorUserID, botUpdateDate(date))
	return s.botUpdates.appendBotOnly(domain.WithChannelAdminBotAudience(changes, s.adminBotMembersLocked(res.Channel.ID)))
}
