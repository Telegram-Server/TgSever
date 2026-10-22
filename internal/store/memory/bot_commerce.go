package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type memoryCommerceCheckout struct{ slug, subscriptionID string }
type BotCommerceStore struct {
	mu                    sync.Mutex
	worker                sync.Mutex
	stars                 *StarsStore
	bots                  *BotStore
	messages              *MessageStore
	channels              *ChannelStore
	updates               store.BotAPIUpdateStore
	offers                map[string]domain.InvoiceLink
	checkouts             map[int64]memoryCommerceCheckout
	subscriptions         map[string]domain.StarSubscription
	media                 map[string]domain.PaidMedia
	purchases             map[string]map[int64]bool
	balances              map[int64]int64
	transactions          map[int64][]domain.BotStarTransaction
	receipts              map[int64]domain.SendPrivateTextResult
	balanceNanos          map[int64]int32
	broadcastReservations map[int64]map[string]domain.BotBroadcastReservation
	paidBroadcastEnabled  map[int64]bool
	broadcastActive       map[int64]map[string]struct{}
}

func NewBotCommerceStore(stars *StarsStore, bots *BotStore, messages *MessageStore, channels *ChannelStore, updates ...store.BotAPIUpdateStore) *BotCommerceStore {
	s := &BotCommerceStore{stars: stars, bots: bots, messages: messages, channels: channels, offers: make(map[string]domain.InvoiceLink), checkouts: make(map[int64]memoryCommerceCheckout), subscriptions: make(map[string]domain.StarSubscription), media: make(map[string]domain.PaidMedia), purchases: make(map[string]map[int64]bool), balances: make(map[int64]int64), transactions: make(map[int64][]domain.BotStarTransaction), receipts: make(map[int64]domain.SendPrivateTextResult)}
	if len(updates) > 0 {
		s.updates = updates[0]
	}
	messages.commerce = s
	if channels != nil {
		channels.commerce = s
	}
	return s
}

var _ store.BotCommerce = (*BotCommerceStore)(nil)

func (s *BotCommerceStore) CreateInvoiceLink(_ context.Context, v domain.InvoiceLink) error {
	if !v.Valid() {
		return domain.ErrCommerceInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.offers[v.Slug]; ok {
		return domain.ErrCommerceInvalid
	}
	s.offers[v.Slug] = v
	return nil
}
func (s *BotCommerceStore) InvoiceLink(_ context.Context, slug string) (domain.InvoiceLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.offers[slug]
	if !ok {
		return v, domain.ErrCommerceNotFound
	}
	return v, nil
}
func (s *BotCommerceStore) SaveSubscriptionInvite(ctx context.Context, v domain.InvoiceLink) error {
	if v.ChannelID <= 0 || v.InviteHash == "" || v.SubscriptionPeriod != domain.StarSubscriptionPeriod {
		return domain.ErrCommerceInvalid
	}
	return s.CreateInvoiceLink(ctx, v)
}
func (s *BotCommerceStore) SubscriptionInvite(_ context.Context, hash string) (domain.InvoiceLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.offers {
		if v.InviteHash == hash {
			return v, nil
		}
	}
	return domain.InvoiceLink{}, domain.ErrCommerceNotFound
}
func (s *BotCommerceStore) CreateInvoiceCheckout(ctx context.Context, slug string, userID int64, date int) (domain.BotInvoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCheckoutLocked(ctx, slug, userID, date, "")
}
func (s *BotCommerceStore) createCheckoutLocked(ctx context.Context, slug string, userID int64, date int, subscriptionID string) (domain.BotInvoice, error) {
	offer, ok := s.offers[slug]
	if !ok || userID <= 0 || date <= 0 {
		return domain.BotInvoice{}, domain.ErrCommerceNotFound
	}
	if offer.InviteHash != "" && subscriptionID == "" {
		checked, err := s.channels.CheckInvite(ctx, userID, offer.InviteHash, date)
		if err != nil {
			return domain.BotInvoice{}, err
		}
		if !checked.Channel.Broadcast {
			return domain.BotInvoice{}, domain.ErrCommerceInvalid
		}
	}
	invoice := domain.BotInvoice{BotUserID: offer.BotID, ChatID: userID, Title: offer.Title, Description: offer.Description, Payload: offer.Payload, Amount: offer.Amount, Currency: "XTR", Date: date}
	s.bots.invoices.mu.Lock()
	defer s.bots.invoices.mu.Unlock()
	s.bots.invoices.nextID++
	invoice.ID = s.bots.invoices.nextID
	s.bots.invoices.linked[invoice.ID] = invoice
	s.checkouts[invoice.ID] = memoryCommerceCheckout{slug: slug, subscriptionID: subscriptionID}
	return invoice, nil
}
func (s *BotCommerceStore) InvoiceCheckout(_ context.Context, slug string, userID, invoiceID int64) (domain.BotInvoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkout, ok := s.checkouts[invoiceID]
	if !ok || checkout.slug != slug {
		return domain.BotInvoice{}, domain.ErrCommerceNotFound
	}
	s.bots.invoices.mu.Lock()
	defer s.bots.invoices.mu.Unlock()
	v, ok := s.bots.invoices.linked[invoiceID]
	if ok && v.ChatID == userID {
		return v, nil
	}
	return domain.BotInvoice{}, domain.ErrCommerceNotFound
}
func (s *BotCommerceStore) createInvoiceLocked(ctx context.Context, invoice domain.BotInvoice, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, domain.BotInvoice, error) {
	if req.SenderUserID != invoice.BotUserID || req.RecipientUserID != invoice.ChatID || req.RecipientBlocked {
		return domain.SendPrivateTextResult{}, invoice, domain.ErrBotInvoiceInvalid
	}
	fingerprint, err := store.PrivateSendFingerprint(req)
	if err != nil {
		return domain.SendPrivateTextResult{}, invoice, err
	}
	broadcast, hasBroadcast := store.BotBroadcastFromContext(ctx)
	if hasBroadcast {
		if broadcast.Store != s || s.validateBroadcastLocked(broadcast) != nil {
			return domain.SendPrivateTextResult{}, invoice, domain.ErrBotBroadcastReservation
		}
	}
	s.messages.mu.Lock()
	defer s.messages.mu.Unlock()
	s.bots.invoices.mu.Lock()
	defer s.bots.invoices.mu.Unlock()
	invoice.MessageID = s.messages.nextBox[invoice.ChatID] + 1
	if !invoice.Valid() {
		return domain.SendPrivateTextResult{}, invoice, domain.ErrBotInvoiceInvalid
	}
	key := botInvoiceKey{invoice.BotUserID, invoice.ChatID, invoice.MessageID}
	invoice.ID = s.bots.invoices.nextID + 1
	sent, err := s.messages.sendPrivateTextLocked(req, fingerprint, &memoryPrivateSendPrefix{apply: func(_ []domain.UpdateEvent) {
		s.bots.invoices.nextID = invoice.ID
		s.bots.invoices.byMsg[key] = invoice
	}})
	if err == nil && hasBroadcast {
		s.commitBroadcastLocked(broadcast, &sent.SenderMessage, nil, sent.Duplicate)
	}
	return sent, invoice, err
}
func (s *MessageStore) CreateBotInvoiceMessage(ctx context.Context, invoice domain.BotInvoice, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if s.commerce == nil {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	s.commerce.mu.Lock()
	defer s.commerce.mu.Unlock()
	sent, _, err := s.commerce.createInvoiceLocked(ctx, invoice, req)
	return sent, err
}
func (s *MessageStore) PayBotInvoice(ctx context.Context, id, userID int64, date int) (domain.SendPrivateTextResult, error) {
	if s.commerce == nil {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	s.commerce.mu.Lock()
	defer s.commerce.mu.Unlock()
	return s.commerce.payInvoiceLocked(ctx, id, userID, date)
}
func (s *BotCommerceStore) payInvoiceLocked(ctx context.Context, id, userID int64, date int) (domain.SendPrivateTextResult, error) {
	checkout := s.checkouts[id]
	offer := s.offers[checkout.slug]
	if offer.ChannelID > 0 {
		s.channels.mu.Lock()
		defer s.channels.mu.Unlock()
	}
	s.messages.mu.Lock()
	defer s.messages.mu.Unlock()
	s.stars.mu.Lock()
	defer s.stars.mu.Unlock()
	s.bots.invoices.mu.Lock()
	defer s.bots.invoices.mu.Unlock()
	var invoice domain.BotInvoice
	var key botInvoiceKey
	for k, v := range s.bots.invoices.byMsg {
		if v.ID == id {
			invoice = v
			key = k
			break
		}
	}
	if invoice.ID == 0 {
		invoice = s.bots.invoices.linked[id]
	}
	if invoice.ID == 0 || invoice.ChatID != userID || date <= 0 {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceNotFound
	}
	if invoice.Refunded {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceRefunded
	}
	if invoice.Paid {
		receipt := s.receipts[id]
		receipt.Duplicate = true
		return receipt, nil
	}
	state := s.stars.states[userID]
	if state == nil || state.balance < invoice.Amount {
		return domain.SendPrivateTextResult{}, domain.ErrStarsInsufficient
	}
	var sub domain.StarSubscription
	if offer.SubscriptionPeriod > 0 {
		sub = domain.StarSubscription{ID: fmt.Sprintf("botsub-%d", id), Slug: offer.Slug, BotID: invoice.BotUserID, UserID: userID, ChannelID: offer.ChannelID, ChargeID: fmt.Sprintf("botinv-%d", id), Period: offer.SubscriptionPeriod, Amount: offer.Amount, Until: date + offer.SubscriptionPeriod, Active: true}
		if checkout.subscriptionID != "" {
			sub = s.subscriptions[checkout.subscriptionID]
			if !sub.Active || sub.Canceled || sub.UserCanceled {
				return domain.SendPrivateTextResult{}, domain.ErrCommerceInvalid
			}
			sub.Until += sub.Period
		}
	}
	if offer.ChannelID > 0 {
		inv, ok := s.channels.invites[offer.InviteHash]
		ch := s.channels.channels[offer.ChannelID]
		if ch.Deleted || (checkout.subscriptionID == "" && (!ok || inv.Revoked || (inv.ExpireDate > 0 && inv.ExpireDate <= date))) {
			return domain.SendPrivateTextResult{}, domain.ErrInviteHashExpired
		}
		if inv.UsageLimit > 0 && inv.UsageCount >= inv.UsageLimit {
			return domain.SendPrivateTextResult{}, domain.ErrUsersTooMuch
		}
		if s.channels.members[offer.ChannelID][userID].IsKicked() {
			return domain.SendPrivateTextResult{}, domain.ErrUserKicked
		}
	}
	charge := fmt.Sprintf("botinv-%d", id)
	req := domain.SendPrivateTextRequest{SenderUserID: userID, RecipientUserID: invoice.BotUserID, RandomID: -id, Date: date, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionPayment, Payment: &domain.MessagePaymentAction{Currency: "XTR", TotalAmount: invoice.Amount, Payload: invoice.Payload, ChargeID: charge, ProviderChargeID: charge, Title: invoice.Title, Description: invoice.Description, IsRecurring: sub.ID != "", IsFirstRecurring: sub.ID != "" && checkout.subscriptionID == "", SubscriptionExpirationDate: sub.Until}}}}
	fingerprint, err := store.PrivateSendFingerprint(req)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	sent, err := s.messages.sendPrivateTextLocked(req, fingerprint, &memoryPrivateSendPrefix{beforeApply: func(_, recipient domain.Message) error {
		if s.updates == nil {
			return nil
		}
		_, _, err := s.updates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{BotUserID: invoice.BotUserID, Kind: domain.BotAPIUpdateMessage, Peer: recipient.Peer, MessageID: recipient.ID, SourcePts: recipient.Pts, Date: date})
		return err
	}, apply: func(_ []domain.UpdateEvent) {
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: invoice.BotUserID}
		if offer.ChannelID > 0 {
			peer = domain.Peer{Type: domain.PeerTypeChannel, ID: offer.ChannelID}
		}
		state.balance -= invoice.Amount
		s.stars.appendTxn(state, userID, -invoice.Amount, domain.StarsReasonBotInvoice, peer, date, invoice.Title, "")
		if offer.OwnerID > 0 && offer.OwnerID != offer.BotID {
			ownerState := s.stars.states[offer.OwnerID]
			if ownerState == nil {
				ownerState = &starsState{}
				s.stars.states[offer.OwnerID] = ownerState
			}
			ownerState.balance += invoice.Amount
			s.stars.appendTxn(ownerState, offer.OwnerID, invoice.Amount, domain.StarsReasonBotInvoice, domain.Peer{Type: domain.PeerTypeUser, ID: userID}, date, invoice.Title, invoice.Payload)
		} else {
			s.creditLocked(invoice.BotUserID, offer.ChannelID, userID, invoice.Amount, domain.StarsReasonBotInvoice, charge, invoice.Payload, offer.SubscriptionPeriod, date)
		}
		invoice.Paid = true
		invoice.PayerID = userID
		invoice.PaidAt = date
		invoice.ChargeID = charge
		if invoice.MessageID == 0 {
			s.bots.invoices.linked[id] = invoice
			s.bots.invoices.linkedCharges[charge] = id
		} else {
			s.bots.invoices.byMsg[key] = invoice
			s.bots.invoices.byCharge[charge] = key
		}
		if sub.ID != "" {
			s.subscriptions[sub.ID] = sub
		}
		if offer.ChannelID > 0 && checkout.subscriptionID == "" {
			// All fallible membership checks happened above, under the same channel lock.
			ch := s.channels.channels[offer.ChannelID]
			inv := s.channels.invites[offer.InviteHash]
			_, _ = s.channels.approveInviteImporterLocked(ch, inv, userID, 0, date)
		}
	}})
	if err == nil {
		s.receipts[id] = sent
	}
	return sent, err
}
func (s *BotCommerceStore) creditLocked(botID, channelID, userID, amount int64, reason domain.StarsTransactionReason, key, payload string, period, date int) {
	if channelID > 0 {
		s.channels.channelStarsBalances[channelID] += amount
		return
	}
	s.balances[botID] += amount
	s.transactions[botID] = append(s.transactions[botID], domain.BotStarTransaction{ID: key, Amount: amount, Date: date, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: userID}, Reason: reason, Payload: payload, SubscriptionPeriod: period})
}
func (s *MessageStore) RefundBotInvoice(_ context.Context, botID, userID int64, charge string, date int) error {
	if s.commerce == nil {
		return domain.ErrBotInvoiceInvalid
	}
	c := s.commerce
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bots.invoices.mu.Lock()
	key, ok := c.bots.invoices.byCharge[charge]
	v := c.bots.invoices.byMsg[key]
	if !ok {
		if id, found := c.bots.invoices.linkedCharges[charge]; found {
			v = c.bots.invoices.linked[id]
			ok = true
		}
	}
	c.bots.invoices.mu.Unlock()
	if !ok || v.BotUserID != botID || v.PayerID != userID {
		return domain.ErrBotInvoiceNotFound
	}
	if v.Refunded {
		return nil
	}
	offer := c.offers[c.checkouts[v.ID].slug]
	if offer.ChannelID > 0 {
		c.channels.mu.Lock()
		defer c.channels.mu.Unlock()
	}
	c.stars.mu.Lock()
	defer c.stars.mu.Unlock()
	balance := c.balances[botID]
	if offer.ChannelID > 0 {
		balance = c.channels.channelStarsBalances[offer.ChannelID]
	}
	if offer.OwnerID > 0 && offer.OwnerID != offer.BotID {
		balance = 0
		if state := c.stars.states[offer.OwnerID]; state != nil {
			balance = state.balance
		}
	}
	if balance < v.Amount {
		return domain.ErrStarsInsufficient
	}
	if offer.OwnerID > 0 && offer.OwnerID != offer.BotID {
		state := c.stars.states[offer.OwnerID]
		state.balance -= v.Amount
		c.stars.appendTxn(state, offer.OwnerID, -v.Amount, domain.StarsReasonBotRefund, domain.Peer{Type: domain.PeerTypeUser, ID: userID}, date, v.Title, v.Payload)
	} else {
		c.creditLocked(botID, offer.ChannelID, userID, -v.Amount, domain.StarsReasonBotRefund, charge, v.Payload, offer.SubscriptionPeriod, date)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: botID}
	if offer.ChannelID > 0 {
		peer = domain.Peer{Type: domain.PeerTypeChannel, ID: offer.ChannelID}
	}
	state := c.stars.states[userID]
	if state == nil {
		state = &starsState{}
		c.stars.states[userID] = state
	}
	state.balance += v.Amount
	c.stars.appendTxn(state, userID, v.Amount, domain.StarsReasonBotRefund, peer, date, v.Title, "")
	v.Refunded = true
	c.bots.invoices.mu.Lock()
	if v.MessageID == 0 {
		c.bots.invoices.linked[v.ID] = v
	} else {
		c.bots.invoices.byMsg[key] = v
	}
	c.bots.invoices.mu.Unlock()
	subID := c.checkouts[v.ID].subscriptionID
	if subID == "" {
		subID = fmt.Sprintf("botsub-%d", v.ID)
	}
	if sub, ok := c.subscriptions[subID]; ok {
		sub.Canceled = true
		sub.UserCanceled = true
		c.subscriptions[subID] = sub
	}
	return nil
}
func (s *BotCommerceStore) EditUserSubscription(_ context.Context, botID, userID int64, charge string, canceled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.subscriptions {
		if v.BotID == botID && v.UserID == userID && v.ChargeID == charge {
			v.Canceled = canceled
			if canceled {
				v.UserCanceled = true
			}
			s.subscriptions[id] = v
			return nil
		}
	}
	return domain.ErrCommerceNotFound
}
func (s *BotCommerceStore) UserSubscriptions(_ context.Context, userID int64) ([]domain.StarSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.StarSubscription, 0)
	for _, v := range s.subscriptions {
		if v.UserID == userID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *BotCommerceStore) ProcessSubscriptions(ctx context.Context, date, limit int) error {
	if date <= 0 || limit < 1 || limit > 1000 {
		return domain.ErrCommerceInvalid
	}
	s.worker.Lock()
	defer s.worker.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0)
	for id, v := range s.subscriptions {
		if v.Active && v.Until <= date {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.subscriptions[ids[i]], s.subscriptions[ids[j]]
		if a.Until != b.Until {
			return a.Until < b.Until
		}
		return a.ID < b.ID
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	for _, id := range ids {
		v := s.subscriptions[id]
		s.stars.mu.Lock()
		state := s.stars.states[v.UserID]
		funded := state != nil && state.balance >= v.Amount
		s.stars.mu.Unlock()
		if !v.Canceled && !v.UserCanceled && funded {
			invoice, err := s.createCheckoutLocked(ctx, v.Slug, v.UserID, date, v.ID)
			if err != nil {
				return err
			}
			if _, err = s.payInvoiceLocked(ctx, invoice.ID, v.UserID, date); err != nil {
				return err
			}
			continue
		}
		v.Active = false
		s.subscriptions[id] = v
		if v.ChannelID > 0 {
			other := false
			for _, entry := range s.subscriptions {
				if entry.ChannelID == v.ChannelID && entry.UserID == v.UserID && entry.Active && entry.Until > date {
					other = true
					break
				}
			}
			if !other {
				if _, err := s.channels.LeaveChannel(ctx, v.ChannelID, v.UserID, date); err != nil && err != domain.ErrUserNotParticipant {
					return err
				}
			}
		}
	}
	return nil
}
func (s *BotCommerceStore) SavePaidMedia(_ context.Context, v domain.PaidMedia) error {
	if v.ID == "" || v.BotID <= 0 || v.OwnerID <= 0 || v.Price < 1 || v.Price > 25000 || len(v.Items) < 1 || len(v.Items) > 10 || len(v.Payload) > 128 {
		return domain.ErrCommerceInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.media[v.ID]; ok {
		return domain.ErrCommerceInvalid
	}
	s.media[v.ID] = *domain.ClonePaidMedia(&v)
	return nil
}
func (s *BotCommerceStore) PaidMedia(_ context.Context, id string, userID int64) (domain.PaidMedia, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.media[id]
	if !ok {
		return v, domain.ErrCommerceNotFound
	}
	v = *domain.ClonePaidMedia(&v)
	v.Unlocked = userID == v.OwnerID || userID == v.BotID || s.purchases[id][userID]
	if !v.Unlocked {
		v = domain.PaidMediaPreview(v)
	}
	return v, nil
}
func (s *BotCommerceStore) PurchasePaidMedia(_ context.Context, id string, userID int64, date int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.media[id]
	if !ok {
		return false, domain.ErrCommerceNotFound
	}
	if userID == v.BotID || userID == v.OwnerID || s.purchases[id][userID] {
		return false, nil
	}
	if v.ChannelID == 0 && userID != v.ChatID {
		return false, domain.ErrPaidMediaLocked
	}
	if v.RevenueChannelID > 0 {
		s.channels.mu.Lock()
		defer s.channels.mu.Unlock()
	}
	s.stars.mu.Lock()
	defer s.stars.mu.Unlock()
	state := s.stars.states[userID]
	if state == nil || state.balance < v.Price {
		return false, domain.ErrStarsInsufficient
	}
	state.balance -= v.Price
	s.stars.appendTxn(state, userID, -v.Price, domain.StarsReasonPaidMedia, domain.Peer{Type: domain.PeerTypeUser, ID: v.BotID}, date, "Paid media", v.Payload)
	s.creditLocked(v.BotID, v.RevenueChannelID, userID, v.Price, domain.StarsReasonPaidMedia, fmt.Sprintf("paidmedia-%s:%d", id, userID), v.Payload, 0, date)
	if s.purchases[id] == nil {
		s.purchases[id] = make(map[int64]bool)
	}
	s.purchases[id][userID] = true
	return true, nil
}
func (s *BotCommerceStore) CanReadPaidFile(_ context.Context, userID int64, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	protected := false
	for id, v := range s.media {
		for _, file := range domain.PaidMediaFileKeys(v) {
			if file == key {
				protected = true
				if userID == v.OwnerID || userID == v.BotID || s.purchases[id][userID] {
					return true, nil
				}
			}
		}
	}
	return !protected, nil
}
func (s *BotCommerceStore) BotStarsBalance(_ context.Context, botID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balances[botID], nil
}
func (s *BotCommerceStore) BotStarTransactions(_ context.Context, botID int64, offset, limit int) ([]domain.BotStarTransaction, error) {
	if botID <= 0 || offset < 0 || limit < 1 || limit > 100 {
		return nil, domain.ErrCommerceInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.transactions[botID]
	out := make([]domain.BotStarTransaction, 0, limit)
	for i := len(rows) - 1 - offset; i >= 0 && len(out) < limit; i-- {
		v := rows[i]
		v.ID = strings.TrimSuffix(v.ID, "-refund")
		out = append(out, v)
	}
	return out, nil
}
func (s *BotCommerceStore) BusinessTransfer(_ context.Context, ownerID, botID, amount int64, date int) error {
	if ownerID <= 0 || botID <= 0 || amount < 1 || amount > 10000 || date <= 0 {
		return domain.ErrCommerceInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stars.mu.Lock()
	defer s.stars.mu.Unlock()
	state := s.stars.states[ownerID]
	if state == nil || state.balance < amount {
		return domain.ErrStarsInsufficient
	}
	state.balance -= amount
	s.stars.appendTxn(state, ownerID, -amount, domain.StarsTransactionReason("business_transfer"), domain.Peer{Type: domain.PeerTypeUser, ID: botID}, date, "Business transfer", "")
	s.creditLocked(botID, 0, ownerID, amount, domain.StarsTransactionReason("business_transfer"), fmt.Sprintf("business-%d-%d", date, len(s.transactions[botID])), "", 0, date)
	return nil
}

func (s *BotCommerceStore) CreateSubscriptionInvite(ctx context.Context, botID, channelID, price int64, name string, date int) (domain.ChannelInvite, error) {
	if price < 1 || price > 10000 {
		return domain.ChannelInvite{}, domain.ErrCommerceInvalid
	}
	// ExportInvite validates both membership and can_invite_users. Hold the
	// commerce lock until the corresponding paid offer becomes visible.
	s.mu.Lock()
	defer s.mu.Unlock()
	view, err := s.channels.GetChannel(ctx, botID, channelID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	if !view.Channel.Broadcast {
		return domain.ChannelInvite{}, domain.ErrCommerceInvalid
	}
	result, err := s.channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: botID, ChannelID: channelID, Title: name, Date: date})
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	offer := domain.InvoiceLink{Slug: "subinvite-" + result.Invite.Hash, BotID: botID, OwnerID: botID, ChannelID: channelID, InviteHash: result.Invite.Hash, Title: "Channel subscription", Description: result.Channel.Title, Payload: result.Invite.Hash, Amount: price, SubscriptionPeriod: domain.StarSubscriptionPeriod, Date: date}
	s.offers[offer.Slug] = offer
	result.Invite.SubscriptionPeriod = offer.SubscriptionPeriod
	result.Invite.SubscriptionPrice = price
	return result.Invite, nil
}

func (s *BotCommerceStore) InvoiceOffer(_ context.Context, invoiceID int64) (domain.InvoiceLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkout, ok := s.checkouts[invoiceID]
	if !ok {
		return domain.InvoiceLink{}, domain.ErrCommerceNotFound
	}
	return s.offers[checkout.slug], nil
}

func (s *BotCommerceStore) ChangeUserSubscription(_ context.Context, userID int64, id string, canceled bool, date int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.subscriptions[id]
	if !ok {
		return domain.ErrCommerceNotFound
	}
	if v.UserID != userID || (!canceled && v.Canceled) {
		return domain.ErrCommerceInvalid
	}
	v.UserCanceled = canceled
	if !canceled && !v.Active {
		v.Active = true
		if v.Until < date {
			v.Until = date
		}
	}
	s.subscriptions[id] = v
	return nil
}
func (s *BotCommerceStore) FulfillSubscription(_ context.Context, userID int64, id string, date int) (domain.CreateChannelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subscriptions[id]
	if !ok {
		return domain.CreateChannelResult{}, domain.ErrCommerceNotFound
	}
	if sub.UserID != userID || !sub.Active || sub.Until <= date || sub.ChannelID <= 0 {
		return domain.CreateChannelResult{}, domain.ErrCommerceInvalid
	}
	s.channels.mu.Lock()
	defer s.channels.mu.Unlock()
	offer := s.offers[sub.Slug]
	channel := s.channels.channels[sub.ChannelID]
	result, err := s.channels.approveInviteImporterLocked(channel, domain.ChannelInvite{ChannelID: sub.ChannelID, AdminUserID: sub.BotID, Hash: offer.InviteHash}, userID, 0, date)
	if err == domain.ErrUserAlreadyParticipant {
		return domain.CreateChannelResult{Channel: channel}, nil
	}
	return result, err
}
