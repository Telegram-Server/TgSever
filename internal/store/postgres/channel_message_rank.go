package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) GetMessageRanks(ctx context.Context, authors []domain.ChannelMessageAuthor) (map[domain.ChannelMessageAuthor]string, error) {
	ranks := make(map[domain.ChannelMessageAuthor]string, len(authors))
	if len(authors) == 0 {
		return ranks, nil
	}
	channelIDs := make([]int64, len(authors))
	userIDs := make([]int64, len(authors))
	for i, author := range authors {
		channelIDs[i] = author.ChannelID
		userIDs[i] = author.UserID
	}
	rows, err := sqlcgen.New(s.db).GetChannelMessageRanks(ctx, sqlcgen.GetChannelMessageRanksParams{ChannelIds: channelIDs, UserIds: userIDs})
	if err != nil {
		return nil, fmt.Errorf("get channel message ranks: %w", err)
	}
	for _, row := range rows {
		ranks[domain.ChannelMessageAuthor{ChannelID: row.ChannelID, UserID: row.UserID}] = row.Rank
	}
	return ranks, nil
}
