package channels

import (
	"context"
	"errors"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 频道身份封禁（#137）的业务入口。
//
// 对外的三个公开方法是其他功能的接入点：
//   - EditBannedSenderChat：封禁/解封一个频道 peer（channels.editBanned 传频道 peer，以及
//     Bot API banChatSenderChat/unbanChatSenderChat 经由同一处理器）。
//   - IsSenderChatBanned：以频道身份发言前的拦截判定（不做查看者鉴权，供发送路径使用）。
//   - GetSenderChatParticipant：单个成员查询里频道 peer 的投影。
// kicked 筛选由 GetParticipants 合并（用户在前、频道在后）。

func (s *Service) bannedSenderChatStore() (store.ChannelBannedSenderChatStore, bool) {
	if s == nil || s.channels == nil {
		return nil, false
	}
	bans, ok := s.channels.(store.ChannelBannedSenderChatStore)
	return bans, ok
}

// EditBannedSenderChat 封禁（BannedRights.ViewMessages）或解封（全空）一个频道 peer。
// 只接受这两种状态，其他组合返回 domain.ErrChannelBannedRightsInvalid；需要群主或有
// ban_users 的管理员，否则 domain.ErrChannelAdminRequired。重复操作返回 Changed=false，
// 不写管理日志。
func (s *Service) EditBannedSenderChat(ctx context.Context, userID int64, req domain.EditChannelBannedSenderChatRequest) (domain.EditChannelBannedSenderChatResult, error) {
	bans, ok := s.bannedSenderChatStore()
	if !ok || userID == 0 {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelInvalid
	}
	if req.UserID == 0 {
		req.UserID = userID
	}
	if req.UserID != userID || req.ChannelID == 0 {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelInvalid
	}
	if req.SenderChatID == 0 {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelSenderChatInvalid
	}
	if _, err := domain.SenderChatBanFromRights(req.BannedRights); err != nil {
		return domain.EditChannelBannedSenderChatResult{}, err
	}
	res, err := bans.EditChannelBannedSenderChat(ctx, req)
	if err == nil && res.Changed {
		s.participantCache.invalidateChannel(req.ChannelID)
	}
	return res, err
}

// IsSenderChatBanned 报告 senderChatID 这个频道是否在 channelID 里被封禁。不做查看者鉴权，
// 供"以频道身份发言"的拦截路径使用；存储不支持该能力时视为未封禁。
func (s *Service) IsSenderChatBanned(ctx context.Context, channelID, senderChatID int64) (bool, error) {
	bans, ok := s.bannedSenderChatStore()
	if !ok || channelID == 0 || senderChatID == 0 {
		return false, nil
	}
	_, banned, err := bans.GetChannelBannedSenderChat(ctx, channelID, senderChatID)
	return banned, err
}

// BannedSenderChatsAmong 返回 senderChatIDs 中在 channelID 里被封禁的那些，一次查询。不做查看者
// 鉴权，供 channels.getSendAs 过滤候选；存储不支持该能力时视为都未封禁。
func (s *Service) BannedSenderChatsAmong(ctx context.Context, channelID int64, senderChatIDs []int64) (map[int64]bool, error) {
	bans, ok := s.bannedSenderChatStore()
	if !ok || channelID == 0 || len(senderChatIDs) == 0 {
		return nil, nil
	}
	ids, err := bans.ListBannedSenderChatIDsAmong(ctx, channelID, senderChatIDs)
	if err != nil {
		return nil, err
	}
	banned := make(map[int64]bool, len(ids))
	for _, id := range ids {
		banned[id] = true
	}
	return banned, nil
}

// GetSenderChatParticipant 返回频道 peer 在群里的成员投影（只有被封禁一种状态）和该频道的
// 基础行。查看者必须是管理员（domain.ErrChannelAdminRequired）；没有封禁记录时返回
// domain.ErrUserNotParticipant。
func (s *Service) GetSenderChatParticipant(ctx context.Context, userID, channelID, senderChatID int64) (domain.ChannelMember, domain.Channel, error) {
	bans, ok := s.bannedSenderChatStore()
	if !ok || userID == 0 || channelID == 0 || senderChatID == 0 {
		return domain.ChannelMember{}, domain.Channel{}, domain.ErrChannelInvalid
	}
	viewer, err := s.channels.GetParticipant(ctx, userID, channelID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotParticipant) {
			return domain.ChannelMember{}, domain.Channel{}, domain.ErrChannelAdminRequired
		}
		return domain.ChannelMember{}, domain.Channel{}, err
	}
	if viewer.Status != domain.ChannelMemberActive || (viewer.Role != domain.ChannelRoleCreator && viewer.Role != domain.ChannelRoleAdmin) {
		return domain.ChannelMember{}, domain.Channel{}, domain.ErrChannelAdminRequired
	}
	record, banned, err := bans.GetChannelBannedSenderChat(ctx, channelID, senderChatID)
	if err != nil {
		return domain.ChannelMember{}, domain.Channel{}, err
	}
	if !banned {
		return domain.ChannelMember{}, domain.Channel{}, domain.ErrUserNotParticipant
	}
	// 频道封禁后可能已被删除：仍返回它的基础行（Deleted=true），让投影能给出这个 peer。
	channels, err := bans.GetSenderChatsByIDs(ctx, []int64{senderChatID})
	if err != nil {
		return domain.ChannelMember{}, domain.Channel{}, err
	}
	var channel domain.Channel
	if len(channels) > 0 {
		channel = channels[0]
	}
	return record.Member(), channel, nil
}

// withBannedSenderChats 把被封禁的频道 peer 接在 kicked 筛选的用户之后：offset/limit 作用在
// "用户在前、频道在后"的合并序列上，Count 为两者之和。users 是存储按同一 offset/limit
// 返回的用户页。
func (s *Service) withBannedSenderChats(ctx context.Context, userID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int, users domain.ChannelParticipantList) (domain.ChannelParticipantList, error) {
	bans, ok := s.bannedSenderChatStore()
	if !ok {
		return users, nil
	}
	chanOffset := max(offset-users.Count, 0)
	chanLimit := max(limit-len(users.Participants), 0)
	page, err := bans.ListChannelBannedSenderChats(ctx, userID, channelID, filter.Query, chanOffset, chanLimit)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	users.Count += page.Count
	for _, ban := range page.Bans {
		users.Participants = append(users.Participants, ban.Member())
	}
	users.SenderChats = append(users.SenderChats, page.SenderChats...)
	return users, nil
}

// senderChatsForAdminLog 补齐管理日志里频道 peer 参与者对应的频道基础行（一次批量读取）。
func (s *Service) senderChatsForAdminLog(ctx context.Context, events []domain.ChannelAdminLogEvent) ([]domain.Channel, error) {
	var ids []int64
	for _, event := range events {
		for _, member := range []*domain.ChannelMember{event.PrevParticipant, event.NewParticipant, event.Participant} {
			if member != nil && member.SenderChatID != 0 {
				ids = append(ids, member.SenderChatID)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	bans, ok := s.bannedSenderChatStore()
	if !ok {
		return nil, nil
	}
	return bans.GetSenderChatsByIDs(ctx, ids)
}
