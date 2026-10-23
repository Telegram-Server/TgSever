package memory

import (
	"context"
	"errors"
	"slices"
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
	if req.SenderChatID == req.ChannelID {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelSenderChatInvalid
	}
	// 目标频道必须存在过；只有封禁要求它未删除，频道删除后封禁记录仍可解除。
	if target, ok := s.channels[req.SenderChatID]; !ok || (ban && target.Deleted) {
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
	s.appendChannelAdminLogLocked(domain.SenderChatBanAdminLogEvent(req, res.Ban, ban, date))
	return res, nil
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
	items := s.bannedSenderChatsMatchingLocked(channelID, query)
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
		out.SenderChats = append(out.SenderChats, s.channels[record.SenderChatID])
	}
	return out, nil
}

// bannedSenderChatsMatchingLocked 返回 query 过滤后的封禁记录（按 SenderChatID 升序）。
// 已删除的频道不计入、不列出（封禁记录保留，仍可解封）。调用方持有 s.mu。
func (s *ChannelStore) bannedSenderChatsMatchingLocked(channelID int64, query string) []domain.ChannelBannedSenderChat {
	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]domain.ChannelBannedSenderChat, 0, len(s.bannedSenderChats[channelID]))
	for _, record := range s.bannedSenderChats[channelID] {
		sender, ok := s.channels[record.SenderChatID]
		if !ok || sender.Deleted {
			continue
		}
		if query != "" && !senderChatMatchesQuery(sender, query) {
			continue
		}
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SenderChatID < items[j].SenderChatID })
	return items
}

// ListBannedSenderChatIDsAmong 返回候选频道中被封禁的那些（见 store.ChannelBannedSenderChatStore）。
func (s *ChannelStore) ListBannedSenderChatIDsAmong(_ context.Context, channelID int64, senderChatIDs []int64) ([]int64, error) {
	if channelID == 0 || len(senderChatIDs) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	bans := s.bannedSenderChats[channelID]
	out := make([]int64, 0, len(senderChatIDs))
	for _, id := range senderChatIDs {
		if _, banned := bans[id]; banned && id != 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ListChannelBannedSenderChatIDs 返回前 limit 条封禁记录的频道 id（见 store.ChannelBannedSenderChatStore）。
func (s *ChannelStore) ListChannelBannedSenderChatIDs(_ context.Context, channelID int64, query string, limit int) ([]int64, error) {
	if channelID == 0 || limit <= 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.bannedSenderChatsMatchingLocked(channelID, query)
	ids := make([]int64, 0, min(limit, len(items)))
	for _, record := range items[:min(limit, len(items))] {
		ids = append(ids, record.SenderChatID)
	}
	return ids, nil
}

func senderChatMatchesQuery(channel domain.Channel, query string) bool {
	return strings.Contains(strings.ToLower(channel.Title), query) || strings.Contains(strings.ToLower(channel.Username), query)
}

// GetSenderChatsByIDs 一次读出一批频道 peer 的基础行（见 store.ChannelBannedSenderChatStore）。
func (s *ChannelStore) GetSenderChatsByIDs(_ context.Context, ids []int64) ([]domain.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[int64]struct{}, len(ids))
	out := make([]domain.Channel, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok || id == 0 {
			continue
		}
		seen[id] = struct{}{}
		if channel, ok := s.channels[id]; ok {
			out = append(out, cloneChannel(channel))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
