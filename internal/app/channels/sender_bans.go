package channels

import (
	"context"
	"telesrv/internal/domain"
)

func (s *Service) GetChannelSenderBan(ctx context.Context, userID, channelID, senderChannelID int64) (domain.ChannelMember, error) {
	if s == nil || s.channels == nil || userID == 0 || channelID == 0 || senderChannelID == 0 {
		return domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	return s.channels.GetChannelSenderBan(ctx, userID, channelID, senderChannelID)
}
