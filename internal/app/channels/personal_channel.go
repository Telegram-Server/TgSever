package channels

import (
	"context"

	"telesrv/internal/domain"
)

// GetPersonalChannelHistory is the explicit public-profile read available to bots.
// Unlike ordinary group history, it is not restricted to bot mentions/replies.
// Store-level viewer authorization and history-clear bounds still apply.
func (s *Service) GetPersonalChannelHistory(ctx context.Context, botUserID, ownerUserID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error) {
	if s == nil || s.channels == nil || s.bots == nil || botUserID == 0 || ownerUserID == 0 || filter.ChannelID == 0 {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	if _, found, err := s.bots.BotInfo(ctx, botUserID); err != nil {
		return domain.ChannelHistory{}, err
	} else if !found {
		return domain.ChannelHistory{}, domain.ErrBotNotFound
	}
	owner, err := s.channels.ResolveChannel(ctx, ownerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	if owner.Self.Status != domain.ChannelMemberActive || owner.Self.Role != domain.ChannelRoleCreator {
		return domain.ChannelHistory{}, domain.ErrChannelAdminRequired
	}
	if !owner.Channel.Broadcast || owner.Channel.Megagroup || owner.Channel.Deleted || owner.Channel.Username == "" {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	filter.Limit = capLimit(filter.Limit, 100)
	return s.channels.ListChannelHistory(ctx, botUserID, filter)
}
