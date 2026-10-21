package memory

import (
	"context"
	"slices"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var _ store.ChannelMemberPager = (*ChannelStore)(nil)

// ListActiveChannelMemberIDsAfter 按 user_id keyset 分页列出活跃成员，不做 viewer 校验。
func (s *ChannelStore) ListActiveChannelMemberIDsAfter(_ context.Context, channelID, afterUserID int64, limit int) ([]int64, error) {
	if limit <= 0 || limit > store.MaxChannelMemberPage {
		limit = store.MaxChannelMemberPage
	}
	s.mu.RLock()
	ids := make([]int64, 0, len(s.members[channelID]))
	for userID, member := range s.members[channelID] {
		if userID > afterUserID && member.Status == domain.ChannelMemberActive {
			ids = append(ids, userID)
		}
	}
	s.mu.RUnlock()
	slices.Sort(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}
