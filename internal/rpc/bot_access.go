package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tgerr"
)

type botAccessChecker interface {
	BotAccessAllowed(ctx context.Context, botID, userID int64) (bool, error)
}

// ensureBotAccess reads current settings before creating an interaction or using a cached grant.
func (r *Router) ensureBotAccess(ctx context.Context, botID, userID int64) error {
	checker, ok := r.deps.Bots.(botAccessChecker)
	if !ok {
		return nil
	}
	allowed, err := checker.BotAccessAllowed(ctx, botID, userID)
	if err != nil {
		return internalErr()
	}
	if !allowed {
		return tgerr.New(403, "BOT_ACCESS_DENIED")
	}
	return nil
}
