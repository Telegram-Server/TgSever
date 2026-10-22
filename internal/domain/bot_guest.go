package domain

import "time"

// BotGuestQuery is a short-lived capability for one explicitly mentioned bot to
// reply to the triggering message. It is unrelated to linked-discussion guests.
type BotGuestQuery struct {
	ID           int64
	BotUserID    int64
	CallerUserID int64
	Peer         Peer
	MessageID    int
	CreatedAt    time.Time
	ExpiresAt    time.Time
}
