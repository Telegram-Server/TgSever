package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var _ store.BotBroadcastStore = (*BotCommerceStore)(nil)

func (s *BotCommerceStore) initBroadcastLocked() {
	if s.balanceNanos == nil {
		s.balanceNanos = make(map[int64]int32)
	}
	if s.broadcastReservations == nil {
		s.broadcastReservations = make(map[int64]map[string]domain.BotBroadcastReservation)
	}
	if s.paidBroadcastEnabled == nil {
		s.paidBroadcastEnabled = make(map[int64]bool)
	}
	if s.broadcastActive == nil {
		s.broadcastActive = make(map[int64]map[string]struct{})
	}
}
func (s *BotCommerceStore) broadcastBalanceLocked(botID int64) domain.StarsAmount {
	return domain.StarsAmount{Amount: s.balances[botID], Nanos: s.balanceNanos[botID]}
}
func (s *BotCommerceStore) setBroadcastBalanceLocked(botID int64, v domain.StarsAmount) {
	s.balances[botID] = v.Amount
	s.balanceNanos[botID] = v.Nanos
}
func (s *BotCommerceStore) BotStarsBalanceExact(_ context.Context, botID int64) (domain.StarsAmount, error) {
	if botID <= 0 {
		return domain.StarsAmount{}, domain.ErrCommerceInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initBroadcastLocked()
	if _, _, err := s.broadcastQuotaLocked(botID, time.Now()); err != nil {
		return domain.StarsAmount{}, err
	}
	return s.broadcastBalanceLocked(botID), nil
}
func cloneBroadcastReservation(v domain.BotBroadcastReservation) domain.BotBroadcastReservation {
	if v.Message != nil {
		msg := cloneMessage(*v.Message)
		v.Message = &msg
	}
	if v.ChannelMessage != nil {
		msg := cloneChannelMessage(*v.ChannelMessage)
		v.ChannelMessage = &msg
	}
	return v
}
func (s *BotCommerceStore) releaseBroadcastLocked(botID int64, key string) error {
	v, ok := s.broadcastReservations[botID][key]
	if !ok || v.State != "reserved" {
		return nil
	}
	balance, err := s.broadcastBalanceLocked(botID).Add(v.Fee)
	if err != nil {
		return err
	}
	s.setBroadcastBalanceLocked(botID, balance)
	v.State = "released"
	s.broadcastReservations[botID][key] = v
	delete(s.broadcastActive[botID], key)
	return nil
}
func (s *BotCommerceStore) broadcastQuotaLocked(botID int64, now time.Time) (int, int, error) {
	count, free := 0, 0
	for key := range s.broadcastActive[botID] {
		v := s.broadcastReservations[botID][key]
		if v.State == "reserved" && !v.Request.Now.Add(domain.BotBroadcastReservationTTL).After(now) {
			if err := s.releaseBroadcastLocked(botID, key); err != nil {
				return 0, 0, err
			}
			continue
		}
		if !v.CountsAt(now) {
			delete(s.broadcastActive[botID], key)
			continue
		}
		count++
		if v.Fee.IsZero() {
			free++
		}
	}
	return count, free, nil
}
func (s *BotCommerceStore) ReserveBotBroadcast(ctx context.Context, req domain.BotBroadcastRequest) (domain.BotBroadcastReservation, error) {
	if !req.Valid() {
		return domain.BotBroadcastReservation{}, domain.ErrBotBroadcastReservation
	}
	profile, found, err := s.bots.GetBot(ctx, req.BotID)
	if err != nil {
		return domain.BotBroadcastReservation{}, err
	}
	if !found || profile.BotUserID != req.BotID {
		return domain.BotBroadcastReservation{}, domain.ErrBotNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initBroadcastLocked()
	if s.broadcastReservations[req.BotID] == nil {
		s.broadcastReservations[req.BotID] = make(map[string]domain.BotBroadcastReservation)
	}
	if s.broadcastActive[req.BotID] == nil {
		s.broadcastActive[req.BotID] = make(map[string]struct{})
	}
	count, free, err := s.broadcastQuotaLocked(req.BotID, req.Now)
	if err != nil {
		return domain.BotBroadcastReservation{}, err
	}
	if v, ok := s.broadcastReservations[req.BotID][req.Key]; ok {
		if v.Request.AllowPaid != req.AllowPaid {
			return domain.BotBroadcastReservation{}, domain.ErrBotBroadcastReservation
		}
		if v.State == "committed" {
			return cloneBroadcastReservation(v), nil
		}
		if v.State == "reserved" {
			return domain.BotBroadcastReservation{}, domain.ErrBotBroadcastInProgress
		}
	}
	if count >= domain.BotBroadcastPaidLimit {
		return domain.BotBroadcastReservation{}, domain.ErrBotBroadcastLimit
	}
	v := domain.BotBroadcastReservation{Request: req, State: "reserved"}
	if free >= domain.BotBroadcastFreeLimit {
		if !req.AllowPaid {
			return v, domain.ErrBotBroadcastLimit
		}
		if !s.paidBroadcastEnabled[req.BotID] {
			return v, domain.ErrBotBroadcastNotEnabled
		}
		balance := s.broadcastBalanceLocked(req.BotID)
		if balance.Less(domain.StarsAmount{Amount: domain.BotBroadcastMinimumStars}) {
			return v, domain.ErrBotBroadcastEligibility
		}
		v.Fee = domain.StarsAmount{Nanos: 100_000_000}
		balance, err = balance.Sub(v.Fee)
		if err != nil {
			return v, err
		}
		s.setBroadcastBalanceLocked(req.BotID, balance)
	}
	s.broadcastReservations[req.BotID][req.Key] = v
	s.broadcastActive[req.BotID][req.Key] = struct{}{}
	return v, nil
}
func (s *BotCommerceStore) ReleaseBotBroadcast(_ context.Context, req domain.BotBroadcastRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.broadcastReservations[req.BotID][req.Key]
	if !ok || !v.Request.Now.Equal(req.Now) {
		return nil
	}
	return s.releaseBroadcastLocked(req.BotID, req.Key)
}
func (s *BotCommerceStore) SetBotPaidBroadcast(ctx context.Context, ownerID, botID int64, enabled bool, date int) error {
	if ownerID <= 0 || botID <= 0 || date <= 0 {
		return domain.ErrCommerceInvalid
	}
	profile, found, err := s.bots.GetBot(ctx, botID)
	if err != nil {
		return err
	}
	if !found || profile.OwnerUserID != ownerID {
		return domain.ErrBotNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initBroadcastLocked()
	if _, _, err := s.broadcastQuotaLocked(botID, time.Unix(int64(date), 0)); err != nil {
		return err
	}
	if enabled && s.broadcastBalanceLocked(botID).Less(domain.StarsAmount{Amount: domain.BotBroadcastMinimumStars}) {
		return domain.ErrBotBroadcastEligibility
	}
	s.paidBroadcastEnabled[botID] = enabled
	return nil
}

// Lock order is commerce -> message/channel, exactly as invoice settlement.
// A missing context does not touch commerce (ordinary invoice callbacks already
// hold that lock). Only the actual successful send may call commit below.
func lockMemoryBroadcast(ctx context.Context) (*BotCommerceStore, store.BotBroadcastContext, error) {
	bc, ok := store.BotBroadcastFromContext(ctx)
	if !ok {
		return nil, bc, nil
	}
	c, ok := bc.Store.(*BotCommerceStore)
	if !ok {
		return nil, bc, domain.ErrBotBroadcastReservation
	}
	c.mu.Lock()
	if err := c.validateBroadcastLocked(bc); err != nil {
		c.mu.Unlock()
		return nil, bc, err
	}
	return c, bc, nil
}
func (s *BotCommerceStore) validateBroadcastLocked(bc store.BotBroadcastContext) error {
	v, ok := s.broadcastReservations[bc.Request.BotID][bc.Request.Key]
	if bc.Now == nil || !ok || !v.Request.Now.Equal(bc.Request.Now) || v.State != "reserved" || !v.Request.Now.Add(domain.BotBroadcastReservationTTL).After(bc.Now()) {
		return domain.ErrBotBroadcastReservation
	}
	// Also prove an exact refund is representable before a possible replay.
	_, err := s.broadcastBalanceLocked(bc.Request.BotID).Add(v.Fee)
	return err
}
func (s *BotCommerceStore) commitBroadcastLocked(bc store.BotBroadcastContext, msg *domain.Message, channel *domain.ChannelMessage, duplicate bool) {
	v := s.broadcastReservations[bc.Request.BotID][bc.Request.Key]
	if duplicate {
		balance, _ := s.broadcastBalanceLocked(bc.Request.BotID).Add(v.Fee) // validated under the same lock
		s.setBroadcastBalanceLocked(bc.Request.BotID, balance)
		v.Fee = domain.StarsAmount{}
	}
	v.State = "committed"
	v.CompletedAt = bc.Now()
	if msg != nil {
		value := cloneMessage(*msg)
		v.Message = &value
	}
	if channel != nil {
		value := cloneChannelMessage(*channel)
		v.ChannelMessage = &value
	}
	if !v.Fee.IsZero() {
		s.transactions[bc.Request.BotID] = append(s.transactions[bc.Request.BotID], domain.BotStarTransaction{ID: "broadcast:" + bc.Request.Key, Amount: -v.Fee.Amount, Nanos: -v.Fee.Nanos, Date: int(v.CompletedAt.Unix()), Reason: domain.StarsTransactionReason("paid_broadcast")})
	}
	s.broadcastReservations[bc.Request.BotID][bc.Request.Key] = v
}
