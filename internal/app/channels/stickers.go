package channels

import (
	"context"
	"telesrv/internal/domain"
)

// SetStickers stores the resolved group sticker set; zero removes it.
func (s *Service) SetStickers(ctx context.Context, userID, channelID, stickerSetID int64) (domain.Channel, error) {
	if s == nil || s.channels == nil || userID == 0 || channelID == 0 || stickerSetID < 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return s.channels.SetStickers(ctx, userID, channelID, stickerSetID)
}
