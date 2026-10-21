package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var _ store.ChannelMemberPager = (*ChannelStore)(nil)

// ListActiveChannelMemberIDsAfter 按 (channel_id, user_id) 主键做 keyset 分页，不做 viewer 校验。
func (s *ChannelStore) ListActiveChannelMemberIDsAfter(ctx context.Context, channelID, afterUserID int64, limit int) ([]int64, error) {
	if limit <= 0 || limit > store.MaxChannelMemberPage {
		limit = store.MaxChannelMemberPage
	}
	ids, err := sqlcgen.New(s.db).ListActiveChannelMemberIDsAfter(ctx, sqlcgen.ListActiveChannelMemberIDsAfterParams{
		ChannelID: channelID,
		UserID:    afterUserID,
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list active channel members after %d: %w", afterUserID, err)
	}
	return ids, nil
}
