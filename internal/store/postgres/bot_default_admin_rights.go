package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

var errBotRightsUnchanged = errors.New("bot default admin rights unchanged")

func (s *BotStore) SetBotDefaultAdminRights(ctx context.Context, botUserID int64, broadcast bool, rights domain.ChannelAdminRights) (bool, error) {
	payload, err := json.Marshal(rights)
	if err != nil {
		return false, err
	}
	if rights == (domain.ChannelAdminRights{}) {
		payload = []byte("{}")
	}
	_, err = s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		n, err := q.SetBotDefaultAdminRights(ctx, sqlcgen.SetBotDefaultAdminRightsParams{BotUserID: botUserID, Broadcast: broadcast, Rights: payload})
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := q.GetBot(ctx, botUserID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return domain.ErrBotNotFound
				}
				return err
			}
			return errBotRightsUnchanged
		}
		return nil
	})
	if errors.Is(err, errBotRightsUnchanged) {
		return false, nil
	}
	return err == nil, err
}
