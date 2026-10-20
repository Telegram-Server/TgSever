package memory

import (
	"context"

	"telesrv/internal/domain"
)

func (s *ChannelStore) GetMessageRanks(_ context.Context, authors []domain.ChannelMessageAuthor) (map[domain.ChannelMessageAuthor]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ranks := make(map[domain.ChannelMessageAuthor]string, len(authors))
	for _, author := range authors {
		member, ok := s.members[author.ChannelID][author.UserID]
		if ok && member.Status == domain.ChannelMemberActive && member.Rank != "" {
			ranks[author] = member.Rank
		}
	}
	return ranks, nil
}
