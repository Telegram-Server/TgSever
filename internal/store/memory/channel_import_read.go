package memory

import (
	"context"
	"slices"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 频道导入的只读查询，语义与 postgres 后端一致（见 store.ChannelImportStore）。

func (s *ChannelStore) PingChannelImportStore(context.Context) error { return nil }

func (s *ChannelStore) ListChannelImports(_ context.Context, filter domain.ChannelImportFilter) ([]domain.ChannelImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ChannelImport
	for _, imp := range s.importStateLocked().imports {
		if filter.State != "" && imp.State != filter.State {
			continue
		}
		if filter.SourceChannelID != 0 && imp.SourceChannelID != filter.SourceChannelID {
			continue
		}
		out = append(out, imp)
	}
	slices.SortFunc(out, func(a, b domain.ChannelImport) int { return int(b.ID - a.ID) })
	return channelImportPage(out, filter.Offset, filter.Limit), nil
}

func channelImportPage[T any](items []T, offset, limit int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	items = items[offset:]
	if limit = store.ChannelImportPageLimit(limit); len(items) > limit {
		items = items[:limit]
	}
	return items
}

func (s *ChannelStore) channelImportSummaryLocked(channel domain.Channel) domain.ChannelImportChannelSummary {
	summary := domain.ChannelImportChannelSummary{Channel: channel}
	for _, msg := range s.messages[channel.ID] {
		if msg.ID > summary.MaxMessageID {
			summary.MaxMessageID = msg.ID
		}
		if !msg.Deleted && msg.ID > summary.LastMessageID {
			summary.LastMessageID = msg.ID
			summary.LastMessageDate = msg.Date
		}
	}
	st := s.importStateLocked()
	if importID, ok := st.byChannel[channel.ID]; ok {
		imp := st.imports[importID]
		summary.Import = &imp
	}
	return summary
}

func (s *ChannelStore) ChannelImportChannelSummaries(_ context.Context, channelIDs []int64) ([]domain.ChannelImportChannelSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ChannelImportChannelSummary, 0, len(channelIDs))
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		channel, ok := s.channels[id]
		if _, dup := seen[id]; !ok || dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, s.channelImportSummaryLocked(channel))
	}
	return out, nil
}

func (s *ChannelStore) SearchChannelImportChannels(_ context.Context, query domain.ChannelImportChannelQuery) ([]domain.ChannelImportChannelSummary, error) {
	title := strings.ToLower(strings.TrimSpace(query.Title))
	username := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query.Username), "@"))
	if title == "" && username == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ChannelImportChannelSummary
	for _, channel := range s.channels {
		if channel.Deleted && !query.IncludeDeleted {
			continue
		}
		byTitle := title != "" && strings.Contains(strings.ToLower(channel.Title), title)
		byUsername := username != "" && strings.ToLower(channel.Username) == username
		if !byTitle && !byUsername {
			continue
		}
		out = append(out, s.channelImportSummaryLocked(channel))
	}
	activity := func(s domain.ChannelImportChannelSummary) int {
		if s.LastMessageDate != 0 {
			return s.LastMessageDate
		}
		return s.Channel.Date
	}
	slices.SortFunc(out, func(a, b domain.ChannelImportChannelSummary) int {
		if d := activity(b) - activity(a); d != 0 {
			return d
		}
		return int(b.Channel.ID - a.Channel.ID)
	})
	return channelImportPage(out, query.Offset, query.Limit), nil
}

func (s *ChannelStore) CountChannelImportChannelMessages(_ context.Context, channelID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, msg := range s.messages[channelID] {
		if !msg.Deleted {
			count++
		}
	}
	return count, nil
}

func (s *ChannelStore) ListChannelImportChannelMessages(_ context.Context, channelID int64, beforeID, limit int) ([]domain.ChannelImportStoredMessage, error) {
	if channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sourceByLocal := make(map[int]int)
	for sourceID, m := range s.importStateLocked().messageMap[channelID] {
		sourceByLocal[m.localID] = sourceID
	}
	var out []domain.ChannelImportStoredMessage
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || (beforeID > 0 && msg.ID >= beforeID) {
			continue
		}
		stored := cloneChannelMessage(msg)
		stored.ViewsCount = max(stored.ViewsCount, s.msgViews[channelID][msg.ID])
		out = append(out, domain.ChannelImportStoredMessage{Message: stored, SourceID: sourceByLocal[msg.ID]})
	}
	slices.SortFunc(out, func(a, b domain.ChannelImportStoredMessage) int { return b.Message.ID - a.Message.ID })
	return channelImportPage(out, 0, limit), nil
}
