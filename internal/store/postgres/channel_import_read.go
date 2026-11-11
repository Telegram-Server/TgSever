package postgres

import (
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// 频道导入的只读查询：任务列表、频道概况与搜索、频道消息。供克隆程序的界面使用。

func (s *ChannelStore) PingChannelImportStore(ctx context.Context) error {
	if _, err := sqlcgen.New(s.db).PingChannelImportStore(ctx); err != nil {
		return fmt.Errorf("ping channel import store: %w", err)
	}
	return nil
}

func (s *ChannelStore) ListChannelImports(ctx context.Context, filter domain.ChannelImportFilter) ([]domain.ChannelImport, error) {
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	rows, err := sqlcgen.New(s.db).ListChannelImports(ctx, sqlcgen.ListChannelImportsParams{
		State:           string(filter.State),
		SourceChannelID: filter.SourceChannelID,
		RowOffset:       int32(filter.Offset),
		RowLimit:        int32(store.ChannelImportPageLimit(filter.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list channel imports: %w", err)
	}
	out := make([]domain.ChannelImport, 0, len(rows))
	for _, row := range rows {
		out = append(out, channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt))
	}
	return out, nil
}

func (s *ChannelStore) ChannelImportChannelSummaries(ctx context.Context, channelIDs []int64) ([]domain.ChannelImportChannelSummary, error) {
	if len(channelIDs) == 0 {
		return nil, nil
	}
	// 直接读库而不经行缓存：界面要看到刚写入的标题、置顶和 pts。
	channels, err := listChannelsByIDs(ctx, s.db, channelIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]domain.Channel, len(channels))
	for _, channel := range channels {
		byID[channel.ID] = channel
	}
	q := sqlcgen.New(s.db)
	stats, err := q.ChannelImportChannelStats(ctx, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("channel import channel stats: %w", err)
	}
	statsByID := make(map[int64]sqlcgen.ChannelImportChannelStatsRow, len(stats))
	for _, row := range stats {
		statsByID[row.ChannelID] = row
	}
	imports, err := q.ListChannelImportsByChannels(ctx, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("list channel imports by channels: %w", err)
	}
	importByChannel := make(map[int64]domain.ChannelImport, len(imports))
	for _, row := range imports {
		importByChannel[row.ChannelID] = channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt)
	}
	out := make([]domain.ChannelImportChannelSummary, 0, len(channelIDs))
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		channel, ok := byID[id]
		if _, dup := seen[id]; !ok || dup {
			continue
		}
		seen[id] = struct{}{}
		summary := domain.ChannelImportChannelSummary{
			Channel:         channel,
			LastMessageID:   int(statsByID[id].LastMessageID),
			LastMessageDate: int(statsByID[id].LastMessageDate),
			MaxMessageID:    int(statsByID[id].MaxMessageID),
		}
		if imp, ok := importByChannel[id]; ok {
			summary.Import = &imp
		}
		out = append(out, summary)
	}
	return out, nil
}

// channelImportLikePattern 把用户输入变成不区分大小写的包含匹配模式：转义 LIKE 的通配符，
// 两端加 %。
func channelImportLikePattern(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
}

func (s *ChannelStore) SearchChannelImportChannels(ctx context.Context, query domain.ChannelImportChannelQuery) ([]domain.ChannelImportChannelSummary, error) {
	pattern := channelImportLikePattern(query.Title)
	username := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query.Username), "@"))
	if pattern == "" && username == "" {
		return nil, nil
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	ids, err := sqlcgen.New(s.db).SearchChannelsForImport(ctx, sqlcgen.SearchChannelsForImportParams{
		IncludeDeleted: query.IncludeDeleted,
		TitlePattern:   pattern,
		Username:       username,
		RowOffset:      int32(query.Offset),
		RowLimit:       int32(store.ChannelImportPageLimit(query.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("search channels for import: %w", err)
	}
	return s.ChannelImportChannelSummaries(ctx, ids)
}

func (s *ChannelStore) CountChannelImportChannelMessages(ctx context.Context, channelID int64) (int, error) {
	count, err := sqlcgen.New(s.db).CountChannelVisibleMessages(ctx, channelID)
	if err != nil {
		return 0, fmt.Errorf("count channel messages: %w", err)
	}
	return int(count), nil
}

func (s *ChannelStore) ListChannelImportChannelMessages(ctx context.Context, channelID int64, beforeID, limit int) ([]domain.ChannelImportStoredMessage, error) {
	if channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if beforeID < 0 {
		beforeID = 0
	}
	page, err := sqlcgen.New(s.db).ListChannelMessageIDsForImport(ctx, sqlcgen.ListChannelMessageIDsForImportParams{
		ChannelID: channelID,
		BeforeID:  int32(beforeID),
		RowLimit:  int32(store.ChannelImportPageLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list channel message ids for import: %w", err)
	}
	if len(page) == 0 {
		return nil, nil
	}
	ids := make([]int32, 0, len(page))
	for _, row := range page {
		ids = append(ids, row.ID)
	}
	// 手写 SQL：消息行的投影与解码是本包共用的 channelMessageColumns / scanChannelMessage
	// （四十多列加若干 JSON 快照），sqlc 生成不了与它一致的行类型；分页与来源 id 的联接已经在
	// 上面的 sqlc 查询里完成，这里只按主键取行。
	rows, err := s.db.Query(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages WHERE channel_id = $1 AND id = ANY($2::int[])`, channelID, ids)
	if err != nil {
		return nil, fmt.Errorf("load channel messages for import: %w", err)
	}
	defer rows.Close()
	byID := make(map[int]domain.ChannelMessage, len(ids))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return nil, err
		}
		byID[msg.ID] = msg
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.ChannelImportStoredMessage, 0, len(page))
	for _, row := range page {
		msg, ok := byID[int(row.ID)]
		if !ok {
			continue
		}
		out = append(out, domain.ChannelImportStoredMessage{Message: msg, SourceID: int(row.SourceID)})
	}
	return out, nil
}
