package domain

import (
	"errors"
	"time"
)

const MessageDraftLifetime = 30 * time.Second

var (
	ErrMessageDraftInvalid       = errors.New("invalid message draft")
	ErrMessageDraftExpired       = errors.New("message draft expired or replaced")
	ErrMessageDraftStopped       = errors.New("message generation stopped")
	ErrMessageDraftStopForbidden = errors.New("message draft cannot be stopped")
)

// MessageDraft is a typing preview, never a message, dialog or PTS record.
// Action is the validated outbound TL action, retained only for keep_on_stop.
type MessageDraft struct {
	BotUserID  int64
	UserID     int64
	ThreadID   int
	ID         int64
	CanStop    bool
	KeepOnStop bool
	Action     []byte
	ExpiresAt  time.Time
}

func (d MessageDraft) Validate(now time.Time) error {
	if d.BotUserID <= 0 || d.UserID <= 0 || d.BotUserID == d.UserID || d.ThreadID < 0 || d.ThreadID > MaxMessageBoxID || d.ID == 0 || len(d.Action) == 0 || len(d.Action) > 2<<20 || !d.ExpiresAt.After(now) || d.ExpiresAt.Sub(now) > MessageDraftLifetime {
		return ErrMessageDraftInvalid
	}
	return nil
}
