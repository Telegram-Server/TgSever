package rpc

import (
	"context"
	"errors"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// botAPIBroadcastMessage wraps one actual outbound message, not a request-sized
// batch. Its callback must retain ctx so the message store commits the reserved
// fee in the same transaction as delivery. The key identifies the send command;
// never derive it from text (two intentional identical sends are not a replay).
func (r *Router) botAPIBroadcastMessage(ctx context.Context, botID int64, key string, allowPaid bool, send func(context.Context) (domain.Message, error)) (out domain.Message, resultErr error) {
	if current, ok := store.BotBroadcastFromContext(ctx); ok {
		if current.Request.BotID != botID {
			return domain.Message{}, domain.ErrBotBroadcastReservation
		}
		return send(ctx)
	}
	if r.deps.BotBroadcast == nil {
		return domain.Message{}, errors.New("BROADCAST_STORE_UNAVAILABLE")
	}
	req := domain.BotBroadcastRequest{BotID: botID, Key: key, AllowPaid: allowPaid, Now: r.clock.Now()}
	reservation, err := r.deps.BotBroadcast.ReserveBotBroadcast(ctx, req)
	if err != nil {
		return domain.Message{}, err
	}
	if reservation.State == "committed" {
		return broadcastReceiptMessage(botID, reservation)
	}
	req = reservation.Request
	// Cancellation must not strand spendable funds. A process crash is recovered
	// by the durable reservation expiry, with commit and reclaim locking the same
	// wallet row so an expired send cannot subsequently deliver without payment.
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if releaseErr := r.deps.BotBroadcast.ReleaseBotBroadcast(releaseCtx, req); releaseErr != nil {
			resultErr = errors.Join(resultErr, releaseErr)
		}
	}()
	msg, err := send(store.WithBotBroadcast(ctx, r.deps.BotBroadcast, req, r.clock.Now))
	if err != nil {
		return domain.Message{}, err
	}
	reservation, err = r.deps.BotBroadcast.ReserveBotBroadcast(ctx, domain.BotBroadcastRequest{BotID: botID, Key: key, AllowPaid: allowPaid, Now: r.clock.Now()})
	if err != nil {
		return domain.Message{}, err
	}
	if reservation.State != "committed" {
		return domain.Message{}, domain.ErrBotBroadcastReservation
	}
	return msg, nil
}
func broadcastReceiptMessage(botID int64, v domain.BotBroadcastReservation) (domain.Message, error) {
	if v.Message != nil {
		return *v.Message, nil
	}
	if v.ChannelMessage != nil {
		return botAPIMessageFromChannel(botID, *v.ChannelMessage), nil
	}
	return domain.Message{}, domain.ErrBotBroadcastReservation
}
