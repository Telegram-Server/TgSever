// Package guestauth binds a consumed guest query to exactly one ordinary send.
package guestauth

import (
	"context"
	"sync/atomic"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type contextKey struct{}
type capability struct {
	query domain.BotGuestQuery
	used  atomic.Bool
}

// Consume is the only constructor for the private, one-use send capability.
// No request field can manufacture this context value.
func Consume(ctx context.Context, queries store.BotGuestQueryStore, botID, queryID int64, now time.Time) (context.Context, domain.BotGuestQuery, error) {
	if queries == nil || botID <= 0 || queryID == 0 {
		return ctx, domain.BotGuestQuery{}, domain.ErrAuthenticatedScopeInvalid
	}
	q, found, err := queries.ConsumeGuestQuery(ctx, botID, queryID, now)
	if err != nil {
		return ctx, domain.BotGuestQuery{}, err
	}
	if !found || q.BotUserID != botID || q.CallerUserID <= 0 || q.Peer.ID <= 0 || !now.Before(q.ExpiresAt) || (q.Peer.Type != domain.PeerTypeUser && q.Peer.Type != domain.PeerTypeChannel) {
		return ctx, domain.BotGuestQuery{}, domain.ErrAuthenticatedScopeInvalid
	}
	return context.WithValue(ctx, contextKey{}, &capability{query: q}), q, nil
}

// Take fails closed for a changed sender/peer and for a second send, even when
// an internal caller accidentally reuses a successfully consumed context.
func Take(ctx context.Context, callerID int64, peer domain.Peer) (int64, error) {
	c, ok := ctx.Value(contextKey{}).(*capability)
	if !ok {
		return 0, nil
	}
	if c.query.CallerUserID != callerID || c.query.Peer != peer || !c.used.CompareAndSwap(false, true) {
		return 0, domain.ErrAuthenticatedScopeInvalid
	}
	return c.query.BotUserID, nil
}
