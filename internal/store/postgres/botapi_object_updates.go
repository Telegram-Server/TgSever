package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *BotAPIUpdateStore) enqueueObjectUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	payload, err := json.Marshal(req.Object)
	if err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	id, err := sqlcgen.New(s.db).EnqueueBotAPIObjectUpdate(ctx, sqlcgen.EnqueueBotAPIObjectUpdateParams{BotUserID: req.BotUserID, UpdateKind: string(req.Kind), Date: int32(req.Date), ObjectPayload: payload})
	if err == pgx.ErrNoRows {
		return domain.BotAPIUpdate{}, false, nil
	}
	if err != nil {
		return domain.BotAPIUpdate{}, false, fmt.Errorf("enqueue bot api object: %w", err)
	}
	return domain.BotAPIUpdate{ID: id, BotUserID: req.BotUserID, Kind: req.Kind, Date: req.Date, Object: req.Object.Clone()}, true, nil
}
