package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func persistPassportAuthorizationTx(ctx context.Context, tx pgx.Tx, req domain.SendPrivateTextRequest) error {
	if req.Media == nil || req.Media.Kind != domain.MessageMediaKindService || req.Media.ServiceAction == nil || req.Media.ServiceAction.Kind != domain.MessageServiceActionPassportData {
		return nil
	}
	a := req.Media.ServiceAction.PassportData
	if a == nil || req.SenderUserID <= 0 || req.RecipientUserID != a.BotUserID || req.SenderUserID == a.BotUserID || req.RecipientBlocked {
		return domain.ErrPassportInvalid
	}
	q := sqlcgen.New(tx)
	if err := q.EnsurePassportData(ctx, req.SenderUserID); err != nil {
		return err
	}
	raw, err := q.LockPassportData(ctx, req.SenderUserID)
	if err != nil {
		return err
	}
	var d domain.PassportUserData
	if err = json.Unmarshal(raw, &d); err != nil {
		return err
	}
	if err = d.Authorize(*a); err != nil {
		return err
	}
	raw, err = json.Marshal(d)
	if err != nil {
		return err
	}
	return q.PutPassportData(ctx, sqlcgen.PutPassportDataParams{UserID: req.SenderUserID, Data: raw})
}

func persistBusinessGameMessageTx(ctx context.Context, tx pgx.Tx, req domain.SendPrivateTextRequest, id int) error {
	if req.Media == nil || req.Media.Kind != domain.MessageMediaKindGame || req.Media.Game == nil || req.BusinessBotID == 0 {
		return nil
	}
	if req.Media.Game.BotUserID != req.BusinessBotID || req.BusinessConnectionID == "" || req.RecipientBlocked {
		return domain.ErrPassportInvalid
	}
	return sqlcgen.New(tx).PutBusinessGameMessageRef(ctx, sqlcgen.PutBusinessGameMessageRefParams{BotUserID: req.BusinessBotID, ChatID: req.RecipientUserID, MessageID: int32(id), OwnerUserID: req.SenderUserID, ConnectionID: req.BusinessConnectionID})
}
