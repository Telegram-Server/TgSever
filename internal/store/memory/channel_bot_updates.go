package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// AttachBotUpdateStore 让成员变动在同一临界区里写 bot qts 记录（#145）。
func (s *ChannelStore) AttachBotUpdateStore(updates *BotUpdateStore) {
	s.botUpdateMu.Lock()
	s.botUpdates = updates
	s.botUpdateMu.Unlock()
}

func botUpdateDate(date int) int {
	if date == 0 {
		return int(time.Now().Unix())
	}
	return date
}

// InviteToChannel invites users and records the bot participants' qts updates.
func (s *ChannelStore) InviteToChannel(_ context.Context, channelID, inviterUserID int64, userIDs []int64, date int) (domain.CreateChannelResult, error) {
	s.botUpdateMu.Lock()
	defer s.botUpdateMu.Unlock()
	res, err := s.inviteToChannel(channelID, inviterUserID, userIDs, date)
	if err != nil {
		return res, err
	}
	res.BotUpdates, err = s.botUpdates.appendBotOnly(domain.BotChannelParticipantChanges(res, inviterUserID, botUpdateDate(date)))
	return res, err
}

// JoinChannel joins a user and records the qts update when the user is a bot.
func (s *ChannelStore) JoinChannel(_ context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error) {
	s.botUpdateMu.Lock()
	defer s.botUpdateMu.Unlock()
	res, err := s.joinChannel(channelID, userID, date)
	if err != nil {
		return res, err
	}
	res.BotUpdates, err = s.botUpdates.appendBotOnly(domain.BotChannelParticipantChanges(res, userID, botUpdateDate(date)))
	return res, err
}

// LeaveChannel leaves a channel and records the qts updates of bot participants
// (the leaver and, for an owner leaving, the successor).
func (s *ChannelStore) LeaveChannel(_ context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error) {
	s.botUpdateMu.Lock()
	defer s.botUpdateMu.Unlock()
	res, err := s.leaveChannel(channelID, userID, date)
	if err != nil {
		return res, err
	}
	res.BotUpdates, err = s.botUpdates.appendBotOnly(domain.BotChannelParticipantChanges(res, userID, botUpdateDate(date)))
	return res, err
}

// EditChannelAdmin edits admin rights and records the qts update when the member is a bot.
func (s *ChannelStore) EditChannelAdmin(_ context.Context, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error) {
	s.botUpdateMu.Lock()
	defer s.botUpdateMu.Unlock()
	s.mu.RLock()
	_, existed := s.members[req.ChannelID][req.MemberID]
	s.mu.RUnlock()
	res, err := s.editChannelAdmin(req)
	if err != nil {
		return res, err
	}
	// 没有成员记录时 Previous 是按"普通成员"合成的占位；对 bot 而言这是从不在群直接被设为管理员。
	previous := res.Previous
	if !existed {
		previous = domain.ChannelMember{}
	}
	if update, ok := domain.BotChannelParticipantChange(res.Channel, req.UserID, previous, res.Participant, botUpdateDate(res.Date)); ok {
		res.BotUpdates, err = s.botUpdates.appendBotOnly([]domain.BotUpdate{update})
	}
	return res, err
}

// EditChannelBanned edits banned rights and records the qts update when the member is a bot.
func (s *ChannelStore) EditChannelBanned(_ context.Context, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error) {
	s.botUpdateMu.Lock()
	defer s.botUpdateMu.Unlock()
	res, err := s.editChannelBanned(req)
	if err != nil {
		return res, err
	}
	if update, ok := domain.BotChannelParticipantChange(res.Channel, req.UserID, res.Previous, res.Participant, botUpdateDate(res.Date)); ok {
		res.BotUpdates, err = s.botUpdates.appendBotOnly([]domain.BotUpdate{update})
	}
	return res, err
}
