package store

import (
	"context"
	"telesrv/internal/domain"
	"time"
)

type BotGuestQueryStore interface {
	CreateGuestQuery(context.Context, domain.BotGuestQuery) error
	GetGuestQuery(ctx context.Context, botID, queryID int64, now time.Time) (domain.BotGuestQuery, bool, error)
	ConsumeGuestQuery(ctx context.Context, botID, queryID int64, now time.Time) (domain.BotGuestQuery, bool, error)
	GuestBotEnabled(ctx context.Context, botID int64) (bool, error)
	SetGuestBotEnabled(ctx context.Context, botID int64, enabled bool) error
}
