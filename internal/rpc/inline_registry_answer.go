package rpc

import (
	"context"
	"time"
)

func (r *inlineRegistry) canAnswerContext(ctx context.Context, now time.Time, botID, queryID int64) bool {
	r.mu.Lock()
	r.pruneLocked(now)
	if q, ok := r.pending[queryID]; ok {
		allowed := q.botUserID == botID && q.results == nil
		r.mu.Unlock()
		return allowed
	}
	r.mu.Unlock()
	if r.shared == nil {
		return false
	}
	q, found, err := r.shared.GetInlinePending(ctx, queryID)
	if err != nil || !found || q.BotUserID != botID || !q.CreatedAt.Add(r.ttl).After(now) {
		return false
	}
	_, answered, err := r.shared.GetInlineResult(ctx, queryID)
	return err == nil && !answered
}
