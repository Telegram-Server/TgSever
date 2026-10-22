package store

import (
	"context"
	"telesrv/internal/domain"
	"time"
)

// MessageDraftStore atomically binds previews and stop tombstones to the bot,
// recipient and thread. All state expires; it is not ordinary message storage.
type MessageDraftStore interface {
	ReplaceMessageDraft(context.Context, domain.MessageDraft, time.Time) error
	StopMessageDraft(ctx context.Context, botID, userID int64, threadID int, draftID int64, now time.Time) (draft domain.MessageDraft, changed bool, err error)
	FinishMessageDraft(ctx context.Context, botID, userID int64, threadID int, now time.Time) error
}
