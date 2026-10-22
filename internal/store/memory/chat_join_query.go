package memory

import (
	"context"
	"telesrv/internal/domain"
)

func (s *ChannelStore) SetChannelGuardBot(_ context.Context, actor, channel, bot int64, enabled bool) (domain.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, err := s.channelForMemberLocked(actor, channel)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(s.members[channel][actor]) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if bot != 0 {
		m := s.members[channel][bot]
		if m.Status != domain.ChannelMemberActive || !canExportChannelInvite(m) {
			return domain.Channel{}, domain.ErrChannelAdminRequired
		}
	}
	ch.JoinRequest = enabled
	if enabled {
		ch.GuardBotID = bot
	} else {
		ch.GuardBotID = 0
	}
	s.channels[channel] = ch
	return cloneChannel(ch), nil
}
func (s *ChannelStore) GetChannelGuardBot(_ context.Context, channel int64) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.channels[channel]
	if !ok || ch.Deleted {
		return 0, nil
	}
	if !ch.JoinRequest || ch.GuardBotID == 0 {
		return 0, nil
	}
	m := s.members[channel][ch.GuardBotID]
	if m.Status != domain.ChannelMemberActive || !canExportChannelInvite(m) {
		return 0, nil
	}
	return ch.GuardBotID, nil
}
func (s *ChannelStore) CreateChatJoinQuery(_ context.Context, q domain.ChatJoinQuery) (domain.ChatJoinQuery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.channels[q.ChannelID]
	m := s.members[q.ChannelID][q.BotUserID]
	pending := s.importers[q.ChannelID][q.UserID]
	if q.ID <= 0 || ch.Deleted || ch.GuardBotID != q.BotUserID || !ch.JoinRequest || m.Status != domain.ChannelMemberActive || !canExportChannelInvite(m) || !pending.Requested || pending.Date != q.RequestDate {
		return domain.ChatJoinQuery{}, domain.ErrChatJoinQueryInvalid
	}
	if s.joinQueries == nil {
		s.joinQueries = make(map[int64]domain.ChatJoinQuery)
	}
	for _, existing := range s.joinQueries {
		if existing.ChannelID == q.ChannelID && existing.UserID == q.UserID && existing.RequestDate == q.RequestDate {
			return existing, nil
		}
	}
	if _, ok := s.joinQueries[q.ID]; ok {
		return domain.ChatJoinQuery{}, domain.ErrChatJoinQueryInvalid
	}
	s.joinQueries[q.ID] = q
	return q, nil
}
func (s *ChannelStore) GetChatJoinQuery(_ context.Context, id int64) (domain.ChatJoinQuery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.joinQueries[id]
	pending := s.importers[q.ChannelID][q.UserID]
	if !ok || !pending.Requested || pending.Date != q.RequestDate {
		return domain.ChatJoinQuery{}, domain.ErrChatJoinQueryInvalid
	}
	return q, nil
}
func (s *ChannelStore) ResolveChatJoinQuery(_ context.Context, bot, id int64, result, url string, now int) (domain.ChatJoinQuery, domain.CreateChannelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.joinQueries[id]
	if !q.CanResolve(bot, now, result) {
		return domain.ChatJoinQuery{}, domain.CreateChannelResult{}, domain.ErrChatJoinQueryInvalid
	}
	ch := s.channels[q.ChannelID]
	m := s.members[q.ChannelID][bot]
	pending := s.importers[q.ChannelID][q.UserID]
	if ch.Deleted || ch.GuardBotID != bot || !ch.JoinRequest || m.Status != domain.ChannelMemberActive || !canExportChannelInvite(m) || !pending.Requested || pending.Date != q.RequestDate {
		return domain.ChatJoinQuery{}, domain.CreateChannelResult{}, domain.ErrChatJoinQueryInvalid
	}
	res := domain.CreateChannelResult{Channel: ch}
	switch result {
	case "webview":
		q.WebAppURL = url
		q.ExpiresAt = now + 300
	case "approve", "decline":
		invite := domain.ChannelInvite{ChannelID: q.ChannelID, AdminUserID: bot}
		if pending.InviteID != 0 {
			var err error
			invite, err = s.inviteByIDLocked(q.ChannelID, pending.InviteID)
			if err != nil {
				return domain.ChatJoinQuery{}, res, err
			}
		}
		if result == "approve" {
			var err error
			res, err = s.approveInviteImporterLocked(ch, invite, q.UserID, bot, now)
			if err != nil {
				return domain.ChatJoinQuery{}, res, err
			}
		} else {
			s.deletePendingInviteImporterLocked(invite, q.UserID)
			res.Recipients = s.activeMemberIDsLocked(q.ChannelID, q.UserID, 0)
		}
	case "queue":
		var invite *domain.ChannelInvite
		if pending.InviteID != 0 {
			value, err := s.inviteByIDLocked(q.ChannelID, pending.InviteID)
			if err != nil {
				return domain.ChatJoinQuery{}, res, err
			}
			invite = &value
		}
		res.JoinRequest = domain.NewChannelJoinRequest(q.ChannelID, q.UserID, q.RequestDate, invite)
	default:
		return domain.ChatJoinQuery{}, res, domain.ErrChatJoinQueryResultInvalid
	}
	q.State = result
	s.joinQueries[id] = q
	return q, res, nil
}

func (s *ChannelStore) ExpireChatJoinQueries(_ context.Context, now, limit int) ([]domain.ChatJoinQueryOutcome, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ChatJoinQueryOutcome
	for id, q := range s.joinQueries {
		if q.ExpiresAt < now-86400 && q.State != "pending" && q.State != "webview" {
			delete(s.joinQueries, id)
			continue
		}
		if len(out) >= limit {
			break
		}
		if q.ExpiresAt > now || (q.State != "pending" && q.State != "webview") {
			continue
		}
		q.State = "queue"
		s.joinQueries[id] = q
		pending := s.importers[q.ChannelID][q.UserID]
		if s.channels[q.ChannelID].Deleted || !pending.Requested || pending.Date != q.RequestDate {
			continue
		}
		var invite *domain.ChannelInvite
		if pending.InviteID != 0 {
			value, err := s.inviteByIDLocked(q.ChannelID, pending.InviteID)
			if err != nil {
				return out, err
			}
			invite = &value
		}
		out = append(out, domain.ChatJoinQueryOutcome{Query: q, Result: domain.CreateChannelResult{Channel: cloneChannel(s.channels[q.ChannelID]), JoinRequest: domain.NewChannelJoinRequest(q.ChannelID, q.UserID, q.RequestDate, invite)}})
	}
	return out, nil
}
