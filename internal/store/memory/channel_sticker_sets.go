package memory

import (
	"context"
	"telesrv/internal/domain"
)

func (s *ChannelStore) SetStickers(_ context.Context, userID, channelID, stickerSetID int64) (domain.Channel, error) {
	if userID == 0 || channelID == 0 || stickerSetID < 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(s.members[channelID][userID]) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if !ch.Megagroup || ch.Broadcast {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if stickerSetID != 0 && ch.ParticipantsCount < domain.MinChannelStickerSetParticipants {
		return domain.Channel{}, domain.ErrChannelParticipantsTooFew
	}
	ch.StickerSetID = stickerSetID
	s.channels[channelID] = ch
	return cloneChannel(ch), nil
}
