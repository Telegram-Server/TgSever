package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *BotAPIUpdateStore) enqueueJoinRequestUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	payload, err := json.Marshal(req.JoinRequest)
	if err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	id, err := sqlcgen.New(s.db).EnqueueBotAPIJoinRequestUpdate(ctx, sqlcgen.EnqueueBotAPIJoinRequestUpdateParams{BotUserID: req.BotUserID, PeerID: req.Peer.ID, Date: int32(req.Date), JoinRequestPayload: payload})
	if err == pgx.ErrNoRows {
		return domain.BotAPIUpdate{}, false, nil
	}
	if err != nil {
		return domain.BotAPIUpdate{}, false, fmt.Errorf("enqueue bot join request: %w", err)
	}
	return domain.BotAPIUpdate{ID: id, BotUserID: req.BotUserID, Kind: req.Kind, Peer: req.Peer, Date: req.Date, JoinRequest: req.JoinRequest.Clone()}, true, nil
}
