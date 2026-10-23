package memory

import (
	"context"
	"sort"

	"telesrv/internal/domain"
)

// appendBotJoinRequestUpdatesLocked 在记录新入群申请的同一临界区（持有 s.mu）里，给有邀请
// 权限的管理员 bot 写 updateBotChatInviteRequester 记录（#149）。invite 为零值表示公开群的
// 入群申请。
func (s *ChannelStore) appendBotJoinRequestUpdatesLocked(channel domain.Channel, invite domain.ChannelInvite, requesterUserID int64, date int) ([]domain.BotUpdate, error) {
	if s.botUpdates == nil {
		return nil, nil
	}
	admins := make([]domain.ChannelMember, 0)
	for _, member := range s.members[channel.ID] {
		if domain.CanReceiveBotJoinRequests(member) {
			admins = append(admins, member)
		}
	}
	if len(admins) == 0 {
		return nil, nil
	}
	sort.Slice(admins, func(i, j int) bool { return admins[i].UserID < admins[j].UserID })
	about := ""
	if s.botUpdates.users != nil {
		if u, ok, _ := s.botUpdates.users.ByID(context.Background(), requesterUserID); ok {
			about = u.About
		}
	}
	return s.botUpdates.appendBotOnly(domain.BotChatInviteRequesterUpdates(channel, invite, requesterUserID, about, admins, botUpdateDate(date)))
}
