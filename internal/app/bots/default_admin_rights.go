package bots

import (
	"context"
	"telesrv/internal/domain"
)

func (s *Service) SetBotDefaultAdminRights(ctx context.Context, botUserID int64, broadcast bool, rights domain.ChannelAdminRights) (bool, error) {
	changed, err := s.bots.SetBotDefaultAdminRights(ctx, botUserID, broadcast, rights)
	if err != nil {
		return false, err
	}
	if changed {
		s.invalidateBotReadCaches(ctx, botUserID)
	}
	return changed, nil
}

// GetBotDefaultAdminRights is the internal read entrypoint for the Bot API gateway.
func (s *Service) GetBotDefaultAdminRights(ctx context.Context, botUserID int64, broadcast bool) (domain.ChannelAdminRights, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return domain.ChannelAdminRights{}, err
	}
	if !found {
		return domain.ChannelAdminRights{}, domain.ErrBotNotFound
	}
	if broadcast {
		return profile.BroadcastAdminRights, nil
	}
	return profile.GroupAdminRights, nil
}
