package memory

import (
	"context"
	"telesrv/internal/domain"
)

func (s *ChannelStore) GetChannelSenderBan(_ context.Context, viewerUserID, channelID, senderChannelID int64) (domain.ChannelMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, viewer, _, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelMember{}, err
	}
	if !isChannelAdmin(viewer) {
		return domain.ChannelMember{}, domain.ErrChannelAdminRequired
	}
	member, ok := s.senderBans[channelID][senderChannelID]
	if !ok {
		return domain.ChannelMember{}, domain.ErrUserNotParticipant
	}
	member.SenderChannelIdentity = domain.SenderIdentity(s.channels[senderChannelID])
	return member, nil
}

func (s *ChannelStore) editChannelSenderBan(req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error) {
	if err := domain.ValidateChannelSenderBan(req); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	if !canBanChannelUsers(s.members[req.ChannelID][req.UserID]) {
		return domain.EditChannelBannedResult{}, domain.ErrChannelAdminRequired
	}
	if sender, ok := s.channels[req.Participant.ID]; !ok || sender.Deleted {
		return domain.EditChannelBannedResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = restrictionNow(0)
	}
	previous, exists := s.senderBans[req.ChannelID][req.Participant.ID]
	if !exists {
		previous = domain.ChannelMember{ChannelID: req.ChannelID, Peer: req.Participant, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft}
	}
	if !exists && !req.BannedRights.ViewMessages {
		return domain.EditChannelBannedResult{NoChange: true, Channel: channel, Previous: previous, Participant: previous, Date: req.Date}, nil
	}
	member := previous
	member.InviterUserID = req.UserID
	member.LeftAt = req.Date
	if req.BannedRights.ViewMessages {
		member = domain.ChannelSenderBanMember(req.ChannelID, req.Participant.ID, req.UserID, req.Date)
		if s.senderBans == nil {
			s.senderBans = make(map[int64]map[int64]domain.ChannelMember)
		}
		if s.senderBans[req.ChannelID] == nil {
			s.senderBans[req.ChannelID] = make(map[int64]domain.ChannelMember)
		}
		s.senderBans[req.ChannelID][req.Participant.ID] = member
	} else {
		delete(s.senderBans[req.ChannelID], req.Participant.ID)
		member.Status = domain.ChannelMemberLeft
		member.BannedRights = domain.ChannelBannedRights{}
	}
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{ChannelID: req.ChannelID, UserID: req.UserID, Date: req.Date, Type: adminLogBanType(previous, member), PrevParticipant: ptrChannelMember(previous), NewParticipant: ptrChannelMember(member)})
	return domain.EditChannelBannedResult{Channel: channel, Previous: previous, Participant: member, Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0), Date: req.Date}, nil
}

func (s *ChannelStore) senderIdentityBannedLocked(channelID, senderID int64) bool {
	owner := s.channels[senderID].CreatorUserID
	for bannedID := range s.senderBans[channelID] {
		if bannedID == senderID || (owner != 0 && s.channels[bannedID].CreatorUserID == owner) {
			return true
		}
	}
	return false
}
