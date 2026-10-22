package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *BotStore) SaveRequestedWebViewButton(ctx context.Context, b domain.BotRequestedWebViewButton) error {
	if b.BotUserID <= 0 || b.UserID <= 0 || b.WebAppReqID == "" || b.ExpiresAt.IsZero() {
		return domain.ErrBotRequestedButtonInvalid
	}
	filter, err := json.Marshal(b.PeerFilter)
	if err != nil {
		return err
	}
	if err = s.q.PruneBotPreparedKeyboardButtons(ctx); err != nil {
		return err
	}
	return s.q.SaveBotPreparedKeyboardButton(ctx, sqlcgen.SaveBotPreparedKeyboardButtonParams{WebappReqID: b.WebAppReqID, BotUserID: b.BotUserID, UserID: b.UserID, ButtonID: int32(b.ButtonID), Text: b.Text, PeerType: b.PeerType, MaxQuantity: int32(b.MaxQuantity), PeerFilter: filter, NameRequested: b.NameRequested, UsernameRequested: b.UsernameRequested, PhotoRequested: b.PhotoRequested, CreatedAt: pgtype.Timestamptz{Time: b.CreatedAt, Valid: true}, ExpiresAt: pgtype.Timestamptz{Time: b.ExpiresAt, Valid: true}, Style: string(b.Style), IconCustomEmojiID: b.IconCustomEmojiID, SuggestedName: b.SuggestedName, SuggestedUsername: b.SuggestedUsername})
}

func requestedKeyboardFromRow(row sqlcgen.WebviewRequestedButton) (domain.BotRequestedWebViewButton, error) {
	b := domain.BotRequestedWebViewButton{WebAppReqID: row.WebappReqID, BotUserID: row.BotUserID, UserID: row.UserID, ButtonID: int(row.ButtonID), Text: row.Text, PeerType: row.PeerType, MaxQuantity: int(row.MaxQuantity), NameRequested: row.NameRequested, UsernameRequested: row.UsernameRequested, PhotoRequested: row.PhotoRequested, CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time, Style: domain.MarkupButtonStyle(row.Style), IconCustomEmojiID: row.IconCustomEmojiID, SuggestedName: row.SuggestedName, SuggestedUsername: row.SuggestedUsername}
	if string(row.PeerFilter) != "null" && len(row.PeerFilter) > 0 {
		if err := json.Unmarshal(row.PeerFilter, &b.PeerFilter); err != nil {
			return domain.BotRequestedWebViewButton{}, err
		}
	}
	return b, nil
}

func (s *BotStore) GetRequestedWebViewButton(ctx context.Context, botID, userID int64, id string) (domain.BotRequestedWebViewButton, bool, error) {
	row, err := s.q.GetBotPreparedKeyboardButton(ctx, sqlcgen.GetBotPreparedKeyboardButtonParams{BotUserID: botID, UserID: userID, WebappReqID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotRequestedWebViewButton{}, false, nil
	}
	if err != nil {
		return domain.BotRequestedWebViewButton{}, false, err
	}
	b, err := requestedKeyboardFromRow(row)
	return b, err == nil, err
}
func (s *BotStore) ConsumeRequestedWebViewButton(ctx context.Context, botID, userID int64, id string) (domain.BotRequestedWebViewButton, bool, error) {
	row, err := s.q.ConsumeBotPreparedKeyboardButton(ctx, sqlcgen.ConsumeBotPreparedKeyboardButtonParams{BotUserID: botID, UserID: userID, WebappReqID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotRequestedWebViewButton{}, false, nil
	}
	if err != nil {
		return domain.BotRequestedWebViewButton{}, false, err
	}
	b, err := requestedKeyboardFromRow(row)
	return b, err == nil, err
}
func (s *BotStore) DeleteRequestedWebViewButton(ctx context.Context, botID, userID int64, id string) error {
	return s.q.DeleteBotPreparedKeyboardButton(ctx, sqlcgen.DeleteBotPreparedKeyboardButtonParams{BotUserID: botID, UserID: userID, WebappReqID: id})
}
