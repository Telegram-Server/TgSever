package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func actorChannelReactions(ctx context.Context, tx pgx.Tx, channelID int64, messageID int, userID int64) ([]domain.MessageReaction, error) {
	q := sqlcgen.New(tx)
	if err := q.LockReactionChannelMessage(ctx, sqlcgen.LockReactionChannelMessageParams{ChannelID: channelID, ID: int32(messageID)}); err != nil {
		return nil, err
	}
	rows, err := q.ListActorChannelReactions(ctx, sqlcgen.ListActorChannelReactionsParams{ChannelID: channelID, MessageID: int32(messageID), ReactedUserID: userID})
	if err != nil {
		return nil, err
	}
	out := make([]domain.MessageReaction, 0, len(rows))
	for _, row := range rows {
		reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(row.ReactionType), row.ReactionValue)
		if !ok {
			return nil, fmt.Errorf("invalid persisted reaction")
		}
		out = append(out, reaction)
	}
	return out, nil
}

func actorChannelReactionChanges(ctx context.Context, tx pgx.Tx, channelID int64, ids []int, userID int64) ([]domain.MessageReactionChange, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := sqlcgen.New(tx)
	id32 := int32s(ids)
	if err := q.LockReactionChannelMessages(ctx, sqlcgen.LockReactionChannelMessagesParams{ChannelID: channelID, MessageIds: id32}); err != nil {
		return nil, err
	}
	rows, err := q.ListActorReactionsForMessages(ctx, sqlcgen.ListActorReactionsForMessagesParams{ChannelID: channelID, UserID: userID, MessageIds: id32})
	if err != nil {
		return nil, err
	}
	changes := make([]domain.MessageReactionChange, 0, len(ids))
	index := make(map[int]int, len(ids))
	for _, id := range ids {
		index[id] = len(changes)
		changes = append(changes, domain.ReactionChangeForActor(id, domain.DecodeReactionActorKey(userID), nil, nil))
	}
	for _, row := range rows {
		reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(row.ReactionType), row.ReactionValue)
		if !ok {
			return nil, fmt.Errorf("invalid persisted reaction")
		}
		i := index[int(row.MessageID)]
		changes[i].OldReactions = append(changes[i].OldReactions, reaction)
	}
	return changes, nil
}
