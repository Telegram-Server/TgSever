package domain

import (
	"errors"
	"math"
	"time"
)

const NanostarsPerStar int32 = 1_000_000_000
const BotBroadcastFreeLimit = 30
const BotBroadcastPaidLimit = 1000

// Bot API 10.3 /bots/api#paid-broadcasts supersedes the older FAQ's
// 100,000 Stars/monthly-active-users eligibility text.
const BotBroadcastMinimumStars int64 = 10_000
const BotBroadcastReservationTTL = 5 * time.Minute

var (
	ErrBotBroadcastLimit            = errors.New("BROADCAST_LIMIT_EXCEEDED")
	ErrBotBroadcastNotEnabled       = errors.New("PAID_BROADCAST_NOT_ENABLED")
	ErrBotBroadcastEligibility      = errors.New("PAID_BROADCAST_NOT_ELIGIBLE")
	ErrBotBroadcastInProgress       = errors.New("BROADCAST_REQUEST_IN_PROGRESS")
	ErrBotBroadcastReservation      = errors.New("BROADCAST_RESERVATION_INVALID")
	ErrBotBroadcastRecipientBlocked = errors.New("USER_IS_BLOCKED")
)

// StarsAmount is an exact, nonnegative amount; no conversion to floating point
// or multiplication of a whole balance by 1e9 is needed.
type StarsAmount struct {
	Amount int64
	Nanos  int32
}

func (v StarsAmount) Valid() bool  { return v.Amount >= 0 && v.Nanos >= 0 && v.Nanos < NanostarsPerStar }
func (v StarsAmount) IsZero() bool { return v.Amount == 0 && v.Nanos == 0 }
func (v StarsAmount) Less(w StarsAmount) bool {
	return v.Amount < w.Amount || (v.Amount == w.Amount && v.Nanos < w.Nanos)
}
func (v StarsAmount) Add(w StarsAmount) (StarsAmount, error) {
	if !v.Valid() || !w.Valid() {
		return StarsAmount{}, ErrStarsInvalidAmount
	}
	nanos := int64(v.Nanos) + int64(w.Nanos)
	carry := nanos / int64(NanostarsPerStar)
	if v.Amount > math.MaxInt64-w.Amount || v.Amount+w.Amount > math.MaxInt64-carry {
		return StarsAmount{}, ErrStarsInvalidAmount
	}
	return StarsAmount{Amount: v.Amount + w.Amount + carry, Nanos: int32(nanos % int64(NanostarsPerStar))}, nil
}
func (v StarsAmount) Sub(w StarsAmount) (StarsAmount, error) {
	if !v.Valid() || !w.Valid() {
		return StarsAmount{}, ErrStarsInvalidAmount
	}
	if v.Less(w) {
		return StarsAmount{}, ErrStarsInsufficient
	}
	out := StarsAmount{Amount: v.Amount - w.Amount, Nanos: v.Nanos - w.Nanos}
	if out.Nanos < 0 {
		out.Amount--
		out.Nanos += NanostarsPerStar
	}
	return out, nil
}

type BotBroadcastRequest struct {
	BotID     int64
	Key       string
	AllowPaid bool
	Now       time.Time
}

func (r BotBroadcastRequest) Valid() bool {
	return r.BotID > 0 && len(r.Key) > 0 && len(r.Key) <= 128 && !r.Now.IsZero()
}

type BotBroadcastReservation struct {
	Request        BotBroadcastRequest
	Fee            StarsAmount
	State          string
	CompletedAt    time.Time
	Message        *Message
	ChannelMessage *ChannelMessage
}

func (r BotBroadcastReservation) CountsAt(now time.Time) bool {
	return r.State == "reserved" || (r.State == "committed" && r.CompletedAt.After(now.Add(-time.Second)))
}
