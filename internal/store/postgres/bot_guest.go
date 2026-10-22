package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type BotGuestQueryStore struct{ q *sqlcgen.Queries }

func NewBotGuestQueryStore(db sqlcgen.DBTX) *BotGuestQueryStore {
	return &BotGuestQueryStore{q: sqlcgen.New(db)}
}
func (s *BotGuestQueryStore) CreateGuestQuery(ctx context.Context, v domain.BotGuestQuery) error {
	if v.ID == 0 || v.BotUserID <= 0 || v.CallerUserID <= 0 || v.MessageID <= 0 || v.Peer.ID <= 0 || (v.Peer.Type != domain.PeerTypeUser && v.Peer.Type != domain.PeerTypeChannel) || !v.ExpiresAt.After(v.CreatedAt) {
		return errors.New("QUERY_ID_INVALID")
	}
	if err := s.q.PruneBotGuestQueries(ctx, pgtype.Timestamptz{Time: v.CreatedAt, Valid: true}); err != nil {
		return err
	}
	return s.q.CreateBotGuestQuery(ctx, sqlcgen.CreateBotGuestQueryParams{ID: v.ID, BotUserID: v.BotUserID, CallerUserID: v.CallerUserID, PeerType: string(v.Peer.Type), PeerID: v.Peer.ID, MessageID: int32(v.MessageID), CreatedAt: pgtype.Timestamptz{Time: v.CreatedAt, Valid: true}, ExpiresAt: pgtype.Timestamptz{Time: v.ExpiresAt, Valid: true}})
}
func guestQueryFromRow(v sqlcgen.BotGuestQuery) domain.BotGuestQuery {
	return domain.BotGuestQuery{ID: v.ID, BotUserID: v.BotUserID, CallerUserID: v.CallerUserID, Peer: domain.Peer{Type: domain.PeerType(v.PeerType), ID: v.PeerID}, MessageID: int(v.MessageID), CreatedAt: v.CreatedAt.Time, ExpiresAt: v.ExpiresAt.Time}
}
func (s *BotGuestQueryStore) GetGuestQuery(ctx context.Context, botID, queryID int64, now time.Time) (domain.BotGuestQuery, bool, error) {
	v, err := s.q.GetBotGuestQuery(ctx, sqlcgen.GetBotGuestQueryParams{ID: queryID, BotUserID: botID, ExpiresAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotGuestQuery{}, false, nil
	}
	if err != nil {
		return domain.BotGuestQuery{}, false, err
	}
	return guestQueryFromRow(v), true, nil
}
func (s *BotGuestQueryStore) ConsumeGuestQuery(ctx context.Context, botID, queryID int64, now time.Time) (domain.BotGuestQuery, bool, error) {
	v, err := s.q.ConsumeBotGuestQuery(ctx, sqlcgen.ConsumeBotGuestQueryParams{ID: queryID, BotUserID: botID, ExpiresAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotGuestQuery{}, false, nil
	}
	if err != nil {
		return domain.BotGuestQuery{}, false, err
	}
	return guestQueryFromRow(v), true, nil
}
func (s *BotGuestQueryStore) GuestBotEnabled(ctx context.Context, botID int64) (bool, error) {
	return s.q.GuestBotEnabled(ctx, botID)
}
func (s *BotGuestQueryStore) SetGuestBotEnabled(ctx context.Context, botID int64, enabled bool) error {
	return s.q.SetGuestBotEnabled(ctx, sqlcgen.SetGuestBotEnabledParams{BotUserID: botID, Enabled: enabled})
}
func (s *BotGuestQueryStore) GetInlineFeedback(ctx context.Context, botID int64) (int, error) {
	v, err := s.q.GetBotInlineFeedback(ctx, botID)
	return int(v), err
}
func (s *BotGuestQueryStore) SetInlineFeedback(ctx context.Context, botID int64, percentage int) error {
	if percentage < 0 || percentage > 100 {
		return errors.New("PERCENTAGE_INVALID")
	}
	return s.q.SetBotInlineFeedback(ctx, sqlcgen.SetBotInlineFeedbackParams{BotUserID: botID, Percentage: int32(percentage)})
}
