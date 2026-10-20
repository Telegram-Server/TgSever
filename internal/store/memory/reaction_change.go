package memory

import "telesrv/internal/domain"

func reactionSelection(rows []domain.ChannelMessagePeerReaction) []domain.MessageReaction {
	out := make([]domain.MessageReaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Reaction)
	}
	return out
}
