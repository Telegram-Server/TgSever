package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// BotBroadcastStore reserves spendable funds in the existing bot revenue wallet.
// Only a successful message-store transaction may commit a reservation.
type BotBroadcastStore interface {
	ReserveBotBroadcast(context.Context, domain.BotBroadcastRequest) (domain.BotBroadcastReservation, error)
	ReleaseBotBroadcast(context.Context, domain.BotBroadcastRequest) error
	BotStarsBalanceExact(context.Context, int64) (domain.StarsAmount, error)
	SetBotPaidBroadcast(context.Context, int64, int64, bool, int) error
}

type botBroadcastContextKey struct{}
type BotBroadcastContext struct {
	Store   BotBroadcastStore
	Request domain.BotBroadcastRequest
	Now     func() time.Time
}

func WithBotBroadcast(ctx context.Context, st BotBroadcastStore, req domain.BotBroadcastRequest, now func() time.Time) context.Context {
	if now == nil {
		now = time.Now
	}
	return context.WithValue(ctx, botBroadcastContextKey{}, BotBroadcastContext{Store: st, Request: req, Now: now})
}
func BotBroadcastFromContext(ctx context.Context) (BotBroadcastContext, bool) {
	v, ok := ctx.Value(botBroadcastContextKey{}).(BotBroadcastContext)
	return v, ok
}
