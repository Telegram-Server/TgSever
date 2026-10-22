package store

import "context"

type BotInlineFeedbackStore interface {
	GetInlineFeedback(ctx context.Context, botID int64) (int, error)
	SetInlineFeedback(ctx context.Context, botID int64, percentage int) error
}
