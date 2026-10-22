package domain

import (
	"errors"
	"time"
)

// BotManagement keeps the manager association separate from the human owner.
type BotManagement struct {
	BotUserID          int64
	ManagerBotID       int64
	IsAccessRestricted bool
	AddedUserIDs       []int64
}

type BotAPIInstanceKey struct {
	BotUserID  int64
	InstanceID string
}
type BotAPIInstance struct {
	BotUserID   int64
	InstanceID  string
	Cloud       bool
	StartedAt   time.Time
	ClosedAt    time.Time
	LoggedOutAt time.Time
}

var ErrBotManagerInvalid = errors.New("BOT_MANAGER_INVALID")
var ErrBotAccessDenied = errors.New("BOT_ACCESS_DENIED")
var ErrBotInstanceClosed = errors.New("BOT_INSTANCE_CLOSED")

type BotAPIInstanceWaitError struct{ Seconds int }

func (e *BotAPIInstanceWaitError) Error() string { return "BOT_INSTANCE_RETRY_AFTER" }
