package memory

import (
	"context"
	"telesrv/internal/domain"
)

func (s *BotStore) SetBotDefaultAdminRights(_ context.Context, botUserID int64, broadcast bool, rights domain.ChannelAdminRights) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[botUserID]
	if !ok {
		return false, domain.ErrBotNotFound
	}
	current := p.GroupAdminRights
	if broadcast {
		current = p.BroadcastAdminRights
	}
	if current == rights {
		return false, nil
	}
	if broadcast {
		p.BroadcastAdminRights = rights
	} else {
		p.GroupAdminRights = rights
	}
	s.byID[botUserID] = p
	s.users.bumpBotInfoVersion(botUserID)
	return true, nil
}
