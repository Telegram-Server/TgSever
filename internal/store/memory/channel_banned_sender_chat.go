package memory

import (
	"context"
	"errors"
	"sort"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var _ store.ChannelBannedSenderChatStore = (*ChannelStore)(nil)

// EditChannelBannedSenderChat 封禁或解封一个频道 peer（见 store.ChannelBannedSenderChatStore）。
func (s *ChannelStore) EditChannelBannedSenderChat(_ context.Context, req domain.EditChannelBannedSenderChatRequest) (domain.EditChannelBannedSenderChatResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.SenderChatID == 0 {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelInvalid
	}
	ban, err := domain.SenderChatBanFromRights(req.BannedRights)
	if err != nil {
		return domain.EditChannelBannedSenderChatResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, actor, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelBannedSenderChatResult{}, err
	}
	if !canBanChannelUsers(actor) {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelAdminRequired
	}
	if target, ok := s.channels[req.SenderChatID]; !ok || target.Deleted || req.SenderChatID == req.ChannelID {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelSenderChatInvalid
	}
	date := restrictionNow(req.Date)
	existing, found := s.bannedSenderChats[req.ChannelID][req.SenderChatID]
	res := domain.EditChannelBannedSenderChatResult{Channel: channel, Banned: ban}
	switch {
	case ban && found:
		res.Ban = existing
		return res, nil
	case !ban && !found:
		return res, nil
	case ban:
		record := domain.ChannelBannedSenderChat{ChannelID: req.ChannelID, SenderChatID: req.SenderChatID, BannedByUserID: req.UserID, Date: date}
		if s.bannedSenderChats == nil {
			s.bannedSenderChats = make(map[int64]map[int64]domain.ChannelBannedSenderChat)
		}
		if s.bannedSenderChats[req.ChannelID] == nil {
			s.bannedSenderChats[req.ChannelID] = make(map[int64]domain.ChannelBannedSenderChat)
		}
		s.bannedSenderChats[req.ChannelID][req.SenderChatID] = record
		res.Ban = record
	default:
		delete(s.bannedSenderChats[req.ChannelID], req.SenderChatID)
		res.Ban = existing
	}
	res.Changed = true
	s.appendChannelAdminLogLocked(senderChatBanAdminLogEvent(req, res.Ban, ban, date))
	return res, nil
}

// senderChatBanAdminLogEvent 生成频道 peer 封禁/解封的管理日志：与用户被踢出/解除踢出同类，
// 参与者用 SenderChatID 表示频道 peer。
func senderChatBanAdminLogEvent(req domain.EditChannelBannedSenderChatRequest, record domain.ChannelBannedSenderChat, banned bool, date int) domain.ChannelAdminLogEvent {
	bannedMember := record.Member()
	left := domain.SenderChatLeftMember(req.ChannelID, req.SenderChatID)
	event := domain.ChannelAdminLogEvent{ChannelID: req.ChannelID, UserID: req.UserID, Date: date}
	if banned {
		event.Type = domain.ChannelAdminLogParticipantKick
		event.PrevParticipant, event.NewParticipant = &left, &bannedMember
	} else {
		event.Type = domain.ChannelAdminLogParticipantUnkick
		event.PrevParticipant, event.NewParticipant = &bannedMember, &left
	}
	return event
}

// GetChannelBannedSenderChat 返回封禁记录，不做查看者鉴权。
func (s *ChannelStore) GetChannelBannedSenderChat(_ context.Context, channelID, senderChatID int64) (domain.ChannelBannedSenderChat, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.bannedSenderChats[channelID][senderChatID]
	return record, ok, nil
}

// ListChannelBannedSenderChats 返回一页封禁记录；非管理员查看者得到空列表。
func (s *ChannelStore) ListChannelBannedSenderChats(_ context.Context, viewerUserID, channelID int64, query string, offset, limit int) (domain.ChannelBannedSenderChatList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, viewer, err := s.channelAndMemberLocked(viewerUserID, channelID)
	if errors.Is(err, domain.ErrChannelInvalid) {
		return domain.ChannelBannedSenderChatList{}, err
	}
	if err != nil || !isChannelAdmin(viewer) {
		return domain.ChannelBannedSenderChatList{}, nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]domain.ChannelBannedSenderChat, 0, len(s.bannedSenderChats[channelID]))
	for _, record := range s.bannedSenderChats[channelID] {
		if query != "" && !senderChatMatchesQuery(s.channels[record.SenderChatID], query) {
			continue
		}
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SenderChatID < items[j].SenderChatID })
	out := domain.ChannelBannedSenderChatList{Count: len(items)}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || offset >= len(items) {
		return out, nil
	}
	end := min(offset+limit, len(items))
	out.Bans = items[offset:end]
	for _, record := range out.Bans {
		if channel, ok := s.channels[record.SenderChatID]; ok && !channel.Deleted {
			out.SenderChats = append(out.SenderChats, channel)
		}
	}
	return out, nil
}

func senderChatMatchesQuery(channel domain.Channel, query string) bool {
	return strings.Contains(strings.ToLower(channel.Title), query) || strings.Contains(strings.ToLower(channel.Username), query)
}
