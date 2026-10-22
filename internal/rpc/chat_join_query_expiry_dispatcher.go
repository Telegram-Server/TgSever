package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// ChatJoinQueryExpiryDispatcher releases unanswered guard queries to ordinary admins.
type ChatJoinQueryExpiryDispatcher struct {
	router *Router
	log    *zap.Logger
}

func NewChatJoinQueryExpiryDispatcher(router *Router, log *zap.Logger) *ChatJoinQueryExpiryDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	return &ChatJoinQueryExpiryDispatcher{router: router, log: log}
}

func (d *ChatJoinQueryExpiryDispatcher) Run(ctx context.Context) {
	if d == nil || d.router == nil || d.router.deps.Channels == nil {
		return
	}
	d.DispatchOnce(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.DispatchOnce(ctx)
		}
	}
}

func (d *ChatJoinQueryExpiryDispatcher) DispatchOnce(ctx context.Context) {
	if d == nil || d.router == nil || ctx.Err() != nil {
		return
	}
	if _, err := d.router.ProcessExpiredChatJoinQueries(ctx, 100); err != nil && ctx.Err() == nil {
		d.log.Warn("expire chat join queries failed", zap.Error(err))
	}
}
