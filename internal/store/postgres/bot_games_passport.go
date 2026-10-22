package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

type GamesPassportStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

var _ store.BotGamesPassportStore = (*GamesPassportStore)(nil)

func NewGamesPassportStore(db sqlcgen.DBTX) *GamesPassportStore {
	return &GamesPassportStore{db: db, q: sqlcgen.New(db)}
}
func (s *GamesPassportStore) CreateBotGame(ctx context.Context, g domain.BotGame) error {
	b, e := json.Marshal(g)
	if e != nil {
		return e
	}
	return s.q.CreateBotGame(ctx, sqlcgen.CreateBotGameParams{ID: g.ID, BotUserID: g.BotUserID, ShortName: g.ShortName, Game: b})
}
func (s *GamesPassportStore) GetBotGame(ctx context.Context, botID int64, name string) (domain.BotGame, bool, error) {
	b, e := s.q.GetBotGame(ctx, sqlcgen.GetBotGameParams{BotUserID: botID, ShortName: name})
	return decodeBotGame(b, e)
}
func (s *GamesPassportStore) GetBotGameByID(ctx context.Context, id int64) (domain.BotGame, bool, error) {
	b, e := s.q.GetBotGameByID(ctx, id)
	return decodeBotGame(b, e)
}
func decodeBotGame(b []byte, e error) (domain.BotGame, bool, error) {
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.BotGame{}, false, nil
	}
	if e != nil {
		return domain.BotGame{}, false, e
	}
	var g domain.BotGame
	e = json.Unmarshal(b, &g)
	return g, e == nil, e
}
func (s *GamesPassportStore) SetBotGameScore(ctx context.Context, key domain.GameScoreScope, userID int64, score int, force bool) error {
	n, e := s.q.SetBotGameScore(ctx, sqlcgen.SetBotGameScoreParams{GameID: key.GameID, ChatID: key.ChatID, InlineOwnerID: key.InlineOwnerID, InlineMessageID: int32(key.InlineMessageID), UserID: userID, Score: int32(score), Force: force})
	if e != nil {
		return e
	}
	if n == 0 {
		return domain.ErrGameScoreNotModified
	}
	return nil
}
func (s *GamesPassportStore) BotGameScores(ctx context.Context, key domain.GameScoreScope) ([]domain.GameScore, error) {
	rows, e := s.q.BotGameScores(ctx, sqlcgen.BotGameScoresParams{GameID: key.GameID, ChatID: key.ChatID, InlineOwnerID: key.InlineOwnerID, InlineMessageID: int32(key.InlineMessageID)})
	if e != nil {
		return nil, e
	}
	out := make([]domain.GameScore, 0, len(rows))
	for i, row := range rows {
		out = append(out, domain.GameScore{UserID: row.UserID, Score: int(row.Score), Position: i + 1})
	}
	return out, nil
}
func (s *GamesPassportStore) GetPassportData(ctx context.Context, id int64) (domain.PassportUserData, error) {
	b, e := s.q.GetPassportData(ctx, id)
	var d domain.PassportUserData
	if errors.Is(e, pgx.ErrNoRows) {
		return d, nil
	}
	if e != nil {
		return d, e
	}
	e = json.Unmarshal(b, &d)
	return d, e
}

// A row lock covers the read/validate/write transition; an error rolls back the
// whole authorization/error batch rather than exposing a partially rejected form.
func (s *GamesPassportStore) mutatePassport(ctx context.Context, id int64, fn func(*domain.PassportUserData) error) error {
	return withTx(ctx, s.db, "passport transition", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if e := q.EnsurePassportData(ctx, id); e != nil {
			return e
		}
		b, e := q.LockPassportData(ctx, id)
		if e != nil {
			return e
		}
		var d domain.PassportUserData
		if e = json.Unmarshal(b, &d); e != nil {
			return e
		}
		if e = fn(&d); e != nil {
			return e
		}
		b, e = json.Marshal(d)
		if e != nil {
			return e
		}
		return q.PutPassportData(ctx, sqlcgen.PutPassportDataParams{UserID: id, Data: b})
	})
}
func (s *GamesPassportStore) SavePassportElement(ctx context.Context, id int64, el domain.PassportElement) error {
	return s.mutatePassport(ctx, id, func(d *domain.PassportUserData) error { d.SaveElement(el); return nil })
}
func (s *GamesPassportStore) DeletePassportElements(ctx context.Context, id int64, types []string) error {
	return s.mutatePassport(ctx, id, func(d *domain.PassportUserData) error { d.DeleteElements(types); return nil })
}
func (s *GamesPassportStore) SetPassportErrors(ctx context.Context, id, botID int64, errs []domain.PassportError) error {
	return s.mutatePassport(ctx, id, func(d *domain.PassportUserData) error { return d.SetErrors(botID, errs) })
}
func (s *GamesPassportStore) SetPassportPublicKey(ctx context.Context, id int64, key string) error {
	return s.q.SetPassportPublicKey(ctx, sqlcgen.SetPassportPublicKeyParams{BotUserID: id, PublicKey: key})
}
func (s *GamesPassportStore) PassportPublicKey(ctx context.Context, id int64) (string, error) {
	key, e := s.q.PassportPublicKey(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", nil
	}
	return key, e
}
func (s *GamesPassportStore) BusinessGameMessageRefs(ctx context.Context, botID, chatID int64, id int) ([]domain.BusinessGameMessageRef, error) {
	rows, e := s.q.BusinessGameMessageRefs(ctx, sqlcgen.BusinessGameMessageRefsParams{BotUserID: botID, ChatID: chatID, MessageID: int32(id)})
	if e != nil {
		return nil, e
	}
	out := make([]domain.BusinessGameMessageRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.BusinessGameMessageRef{BotUserID: botID, ChatID: chatID, MessageID: id, OwnerUserID: row.OwnerUserID, ConnectionID: row.ConnectionID})
	}
	return out, nil
}
