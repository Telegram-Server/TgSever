package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *BotAPIUpdateStore) enqueueReactionUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	payload, err := json.Marshal(req.Reaction)
	if err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	id, err := sqlcgen.New(s.db).EnqueueBotAPIReactionUpdate(ctx, sqlcgen.EnqueueBotAPIReactionUpdateParams{BotUserID: req.BotUserID, UpdateKind: string(req.Kind), PeerType: string(req.Peer.Type), PeerID: req.Peer.ID, MessageID: int32(req.MessageID), Date: int32(req.Date), ReactionPayload: payload})
	if err == pgx.ErrNoRows {
		return domain.BotAPIUpdate{}, false, nil
	}
	if err != nil {
		return domain.BotAPIUpdate{}, false, fmt.Errorf("enqueue bot reaction: %w", err)
	}
	return domain.BotAPIUpdate{ID: id, BotUserID: req.BotUserID, Kind: req.Kind, Peer: req.Peer, MessageID: req.MessageID, Date: req.Date, Reaction: req.Reaction.Clone()}, true, nil
}
