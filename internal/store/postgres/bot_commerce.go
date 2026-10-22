package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

type BotCommerceStore struct{ db sqlcgen.DBTX }

func NewBotCommerceStore(db sqlcgen.DBTX) *BotCommerceStore { return &BotCommerceStore{db: db} }

var _ store.BotCommerce = (*BotCommerceStore)(nil)

func commerceDecode[T any](data []byte, err error) (T, error) {
	var out T
	if errors.Is(err, pgx.ErrNoRows) {
		return out, domain.ErrCommerceNotFound
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}
func (s *BotCommerceStore) CreateInvoiceLink(ctx context.Context, v domain.InvoiceLink) error {
	if !v.Valid() {
		return domain.ErrCommerceInvalid
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sqlcgen.New(s.db).PutCommerceOffer(ctx, sqlcgen.PutCommerceOfferParams{Slug: v.Slug, BotID: v.BotID, InviteHash: v.InviteHash, Data: data})
}
func (s *BotCommerceStore) InvoiceLink(ctx context.Context, slug string) (domain.InvoiceLink, error) {
	return commerceDecode[domain.InvoiceLink](sqlcgen.New(s.db).GetCommerceOffer(ctx, slug))
}
func (s *BotCommerceStore) SaveSubscriptionInvite(ctx context.Context, v domain.InvoiceLink) error {
	if v.ChannelID <= 0 || v.InviteHash == "" || v.SubscriptionPeriod != domain.StarSubscriptionPeriod {
		return domain.ErrCommerceInvalid
	}
	return s.CreateInvoiceLink(ctx, v)
}
func (s *BotCommerceStore) SubscriptionInvite(ctx context.Context, hash string) (domain.InvoiceLink, error) {
	return commerceDecode[domain.InvoiceLink](sqlcgen.New(s.db).GetCommerceInviteOffer(ctx, hash))
}
func (s *BotCommerceStore) CreateInvoiceCheckout(ctx context.Context, slug string, userID int64, date int) (domain.BotInvoice, error) {
	var out domain.BotInvoice
	err := withTx(ctx, s.db, "create invoice link checkout", func(tx pgx.Tx) error {
		offer, err := NewBotCommerceStore(tx).InvoiceLink(ctx, slug)
		if err != nil {
			return err
		}
		out, err = createCommerceCheckoutTx(ctx, tx, offer, userID, date, "")
		return err
	})
	return out, err
}
func createCommerceCheckoutTx(ctx context.Context, tx pgx.Tx, offer domain.InvoiceLink, userID int64, date int, subscriptionID string) (domain.BotInvoice, error) {
	if userID <= 0 || date <= 0 {
		return domain.BotInvoice{}, domain.ErrCommerceInvalid
	}
	if err := lockUsersForUpdate(ctx, tx, offer.BotID, userID); err != nil {
		return domain.BotInvoice{}, err
	}
	if offer.InviteHash != "" && subscriptionID == "" {
		ch, inv, err := NewChannelStore(tx).getInviteByHashForUpdate(ctx, tx, offer.InviteHash)
		if err != nil {
			return domain.BotInvoice{}, err
		}
		if !ch.Broadcast || inv.Revoked || (inv.ExpireDate > 0 && inv.ExpireDate <= date) {
			return domain.BotInvoice{}, domain.ErrInviteHashExpired
		}
	}
	invoice := domain.BotInvoice{BotUserID: offer.BotID, ChatID: userID, Title: offer.Title, Description: offer.Description, Payload: offer.Payload, Amount: offer.Amount, Currency: "XTR", Date: date}
	id, err := sqlcgen.New(tx).CreateCommerceLinkInvoice(ctx, sqlcgen.CreateCommerceLinkInvoiceParams{BotID: offer.BotID, UserID: userID, Title: offer.Title, Description: offer.Description, Amount: offer.Amount, Payload: offer.Payload, Date: int32(date), Slug: offer.Slug})
	if err != nil {
		return domain.BotInvoice{}, err
	}
	invoice.ID = id
	err = sqlcgen.New(tx).PutCommerceCheckout(ctx, sqlcgen.PutCommerceCheckoutParams{InvoiceID: invoice.ID, Slug: offer.Slug, SubscriptionID: subscriptionID})
	return invoice, err
}
func (s *BotCommerceStore) InvoiceCheckout(ctx context.Context, slug string, userID, invoiceID int64) (domain.BotInvoice, error) {
	return commerceDecode[domain.BotInvoice](sqlcgen.New(s.db).GetCommerceInvoice(ctx, sqlcgen.GetCommerceInvoiceParams{Slug: slug, UserID: userID, InvoiceID: invoiceID}))
}
func putCommerceSubscription(ctx context.Context, tx sqlcgen.DBTX, v domain.StarSubscription) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sqlcgen.New(tx).PutCommerceSubscription(ctx, sqlcgen.PutCommerceSubscriptionParams{ID: v.ID, BotID: v.BotID, UserID: v.UserID, ChannelID: v.ChannelID, UntilDate: int32(v.Until), Active: v.Active, Data: data})
}

// Called inside the invoice settlement transaction, after both ledger writes.
func activateCommerceInvoiceTx(ctx context.Context, tx pgx.Tx, invoice domain.BotInvoice, date int) error {
	checkout, err := sqlcgen.New(tx).GetCommerceCheckout(ctx, invoice.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	offer, err := NewBotCommerceStore(tx).InvoiceLink(ctx, checkout.Slug)
	if err != nil {
		return err
	}
	if offer.SubscriptionPeriod == 0 {
		return nil
	}
	id := checkout.SubscriptionID
	v := domain.StarSubscription{ID: fmt.Sprintf("botsub-%d", invoice.ID), Slug: offer.Slug, BotID: offer.BotID, UserID: invoice.ChatID, ChannelID: offer.ChannelID, ChargeID: fmt.Sprintf("botinv-%d", invoice.ID), Period: offer.SubscriptionPeriod, Amount: offer.Amount, Until: date + offer.SubscriptionPeriod, Active: true}
	if id != "" {
		v, err = commerceDecode[domain.StarSubscription](sqlcgen.New(tx).LockCommerceSubscription(ctx, id))
		if err != nil {
			return err
		}
		if v.Canceled || v.UserCanceled || !v.Active {
			return domain.ErrCommerceInvalid
		}
		v.Until += v.Period
	}
	if offer.ChannelID > 0 && id == "" {
		channels := NewChannelStore(tx)
		ch, inv, err := channels.getInviteByHashForUpdate(ctx, tx, offer.InviteHash)
		if err != nil {
			return err
		}
		if _, err = channels.approveInviteImporterTx(ctx, tx, ch, inv, invoice.ChatID, 0, date); err != nil && !errors.Is(err, domain.ErrUserAlreadyParticipant) {
			return err
		}
	}
	return putCommerceSubscription(ctx, tx, v)
}
func (s *BotCommerceStore) EditUserSubscription(ctx context.Context, botID, userID int64, charge string, canceled bool) error {
	return withTx(ctx, s.db, "edit bot subscription", func(tx pgx.Tx) error {
		v, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).FindCommerceSubscription(ctx, sqlcgen.FindCommerceSubscriptionParams{BotID: botID, UserID: userID, ChargeID: charge}))
		if err != nil {
			return err
		}
		v.Canceled = canceled
		if canceled {
			v.UserCanceled = true
		}
		return putCommerceSubscription(ctx, tx, v)
	})
}
func (s *BotCommerceStore) UserSubscriptions(ctx context.Context, userID int64) ([]domain.StarSubscription, error) {
	rows, err := sqlcgen.New(s.db).ListCommerceUserSubscriptions(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.StarSubscription, 0, len(rows))
	for _, data := range rows {
		v, err := commerceDecode[domain.StarSubscription](data, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *BotCommerceStore) ProcessSubscriptions(ctx context.Context, date, limit int) error {
	if date <= 0 || limit < 1 || limit > 1000 {
		return domain.ErrCommerceInvalid
	}
	ids, err := sqlcgen.New(s.db).ListDueCommerceSubscriptions(ctx, sqlcgen.ListDueCommerceSubscriptionsParams{Date: int32(date), MaxRows: int32(limit)})
	if err != nil {
		return err
	}
	for _, id := range ids {
		err = withTx(ctx, s.db, "renew bot subscription", func(tx pgx.Tx) error {
			initial, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).GetCommerceSubscription(ctx, id))
			if err != nil {
				return err
			}
			if err := lockUsersForUpdate(ctx, tx, initial.UserID, initial.BotID); err != nil {
				return err
			}
			v, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).LockCommerceSubscription(ctx, id))
			if err != nil {
				return err
			}
			if !v.Active || v.Until > date {
				return nil
			}
			expire := func() error {
				v.Active = false
				if v.ChannelID > 0 {
					other, err := sqlcgen.New(tx).HasCommerceChannelSubscription(ctx, sqlcgen.HasCommerceChannelSubscriptionParams{ChannelID: v.ChannelID, UserID: v.UserID, Date: int32(date)})
					if err != nil {
						return err
					}
					if !other {
						if _, err := NewChannelStore(tx).LeaveChannel(ctx, v.ChannelID, v.UserID, date); err != nil && !errors.Is(err, domain.ErrUserNotParticipant) {
							return err
						}
					}
				}
				return putCommerceSubscription(ctx, tx, v)
			}
			if v.Canceled || v.UserCanceled {
				return expire()
			}
			balance, err := NewStarsStore(tx).GetBalance(ctx, v.UserID)
			if err != nil {
				return err
			}
			if balance.Balance < v.Amount {
				return expire()
			}
			offer, err := NewBotCommerceStore(tx).InvoiceLink(ctx, v.Slug)
			if err != nil {
				return err
			}
			invoice, err := createCommerceCheckoutTx(ctx, tx, offer, v.UserID, date, v.ID)
			if err != nil {
				return err
			}
			_, err = NewMessageStore(tx).PayBotInvoice(ctx, invoice.ID, v.UserID, date)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *BotCommerceStore) SavePaidMedia(ctx context.Context, v domain.PaidMedia) error {
	if v.ID == "" || v.BotID <= 0 || v.OwnerID <= 0 || v.Price < 1 || v.Price > 25000 || len(v.Items) < 1 || len(v.Items) > 10 || len(v.Payload) > 128 {
		return domain.ErrCommerceInvalid
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return withTx(ctx, s.db, "save paid media", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.PutPaidMedia(ctx, sqlcgen.PutPaidMediaParams{ID: v.ID, BotID: v.BotID, OwnerID: v.OwnerID, Data: data}); err != nil {
			return err
		}
		for _, key := range domain.PaidMediaFileKeys(v) {
			if err := q.PutPaidMediaFile(ctx, sqlcgen.PutPaidMediaFileParams{MediaID: v.ID, LocationKey: key}); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *BotCommerceStore) PaidMedia(ctx context.Context, id string, userID int64) (domain.PaidMedia, error) {
	v, err := commerceDecode[domain.PaidMedia](sqlcgen.New(s.db).GetPaidMedia(ctx, id))
	if err != nil {
		return v, err
	}
	v.Unlocked = userID == v.BotID || userID == v.OwnerID
	if !v.Unlocked {
		v.Unlocked, err = sqlcgen.New(s.db).HasPaidMediaPurchase(ctx, sqlcgen.HasPaidMediaPurchaseParams{MediaID: id, UserID: userID})
	}
	if !v.Unlocked {
		v = domain.PaidMediaPreview(v)
	}
	return v, err
}
func (s *BotCommerceStore) PurchasePaidMedia(ctx context.Context, id string, userID int64, date int) (bool, error) {
	purchased := false
	err := withTx(ctx, s.db, "purchase paid media", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		v, err := commerceDecode[domain.PaidMedia](q.LockPaidMedia(ctx, id))
		if err != nil {
			return err
		}
		if userID == v.OwnerID || userID == v.BotID {
			return nil
		}
		if v.ChannelID == 0 && userID != v.ChatID {
			return domain.ErrPaidMediaLocked
		}
		exists, err := q.HasPaidMediaPurchase(ctx, sqlcgen.HasPaidMediaPurchaseParams{MediaID: id, UserID: userID})
		if err != nil || exists {
			return err
		}
		if _, err := NewStarsStore(tx).Debit(ctx, userID, v.Price, domain.StarsReasonPaidMedia, domain.Peer{Type: domain.PeerTypeUser, ID: v.BotID}, date, "Paid media", v.Payload); err != nil {
			return err
		}
		if err := commerceCreditTx(ctx, tx, v.BotID, v.RevenueChannelID, userID, v.Price, domain.StarsReasonPaidMedia, "paidmedia-"+id+fmt.Sprint(":", userID), date); err != nil {
			return err
		}
		if err := q.PutPaidMediaPurchase(ctx, sqlcgen.PutPaidMediaPurchaseParams{MediaID: id, UserID: userID, Amount: v.Price, Date: int32(date)}); err != nil {
			return err
		}
		purchased = true
		return nil
	})
	return purchased, err
}
func commerceCreditTx(ctx context.Context, tx pgx.Tx, botID, channelID, userID, amount int64, reason domain.StarsTransactionReason, key string, date int) error {
	q := sqlcgen.New(tx)
	if channelID > 0 {
		if err := q.CommerceCreditChannelBalance(ctx, sqlcgen.CommerceCreditChannelBalanceParams{ChannelID: channelID, Amount: amount}); err != nil {
			return err
		}
		return q.CommerceChannelTransaction(ctx, sqlcgen.CommerceChannelTransactionParams{ChannelID: channelID, UserID: userID, Amount: amount, Reason: string(reason), Date: int32(date)})
	}
	if err := q.CommerceCreditBotBalance(ctx, sqlcgen.CommerceCreditBotBalanceParams{BotID: botID, Amount: amount}); err != nil {
		return err
	}
	return q.CommerceBotTransaction(ctx, sqlcgen.CommerceBotTransactionParams{BotID: botID, UserID: userID, Amount: amount, Reason: string(reason), InvoiceKey: key, Date: int32(date)})
}
func (s *BotCommerceStore) CanReadPaidFile(ctx context.Context, userID int64, key string) (bool, error) {
	allowed, err := sqlcgen.New(s.db).CanReadPaidMediaFile(ctx, sqlcgen.CanReadPaidMediaFileParams{UserID: userID, LocationKey: key})
	return allowed != nil && *allowed, err
}
func (s *BotCommerceStore) BotStarsBalance(ctx context.Context, botID int64) (int64, error) {
	return (&StarGiftLifecycleStore{db: s.db}).BotStarsBalance(ctx, botID)
}
func (s *BotCommerceStore) BotStarTransactions(ctx context.Context, botID int64, offset, limit int) ([]domain.BotStarTransaction, error) {
	if botID <= 0 || offset < 0 || limit < 1 || limit > 100 {
		return nil, domain.ErrCommerceInvalid
	}
	rows, err := sqlcgen.New(s.db).CommerceWalletTransactions(ctx, sqlcgen.CommerceWalletTransactionsParams{BotID: botID, SkipRows: int32(offset), MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.BotStarTransaction, 0, len(rows))
	for _, data := range rows {
		v, err := commerceDecode[domain.BotStarTransaction](data, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *BotCommerceStore) BusinessTransfer(ctx context.Context, ownerID, botID, amount int64, date int) error {
	if ownerID <= 0 || botID <= 0 || amount < 1 || amount > 10000 || date <= 0 {
		return domain.ErrCommerceInvalid
	}
	return withTx(ctx, s.db, "transfer business stars", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, ownerID, botID); err != nil {
			return err
		}
		if _, err := NewStarsStore(tx).Debit(ctx, ownerID, amount, domain.StarsTransactionReason("business_transfer"), domain.Peer{Type: domain.PeerTypeUser, ID: botID}, date, "Business transfer", ""); err != nil {
			return err
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		return commerceCreditTx(ctx, tx, botID, 0, ownerID, amount, domain.StarsTransactionReason("business_transfer"), "business-"+hex.EncodeToString(nonce[:]), date)
	})
}

func commerceInvoiceOfferTx(ctx context.Context, tx pgx.Tx, invoiceID int64) (domain.InvoiceLink, error) {
	checkout, err := sqlcgen.New(tx).GetCommerceCheckout(ctx, invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceLink{}, nil
	}
	if err != nil {
		return domain.InvoiceLink{}, err
	}
	return NewBotCommerceStore(tx).InvoiceLink(ctx, checkout.Slug)
}
func creditCommerceInvoiceTx(ctx context.Context, tx pgx.Tx, v domain.BotInvoice, date int) error {
	offer, err := commerceInvoiceOfferTx(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	if offer.OwnerID > 0 && offer.OwnerID != offer.BotID {
		_, err := NewStarsStore(tx).Credit(ctx, offer.OwnerID, v.Amount, domain.StarsReasonBotInvoice, domain.Peer{Type: domain.PeerTypeUser, ID: v.ChatID}, date, v.Title, v.Payload)
		return err
	}
	if offer.ChannelID > 0 {
		return commerceCreditTx(ctx, tx, v.BotUserID, offer.ChannelID, v.ChatID, v.Amount, domain.StarsReasonBotInvoice, fmt.Sprintf("botinv-%d", v.ID), date)
	}
	_, err = (&StarGiftLifecycleStore{db: tx}).applyBotStarsWalletTx(ctx, tx, domain.BotStarsCredit{BotUserID: v.BotUserID, PayerUserID: v.ChatID, Amount: v.Amount, Reason: domain.StarsReasonBotInvoice, InvoiceKey: fmt.Sprintf("botinv-%d", v.ID), Date: date})
	return err
}
func refundCommerceInvoiceTx(ctx context.Context, tx pgx.Tx, v domain.BotInvoice, date int) error {
	offer, err := commerceInvoiceOfferTx(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	if offer.ChannelID > 0 {
		if err := commerceCreditTx(ctx, tx, v.BotUserID, offer.ChannelID, v.PayerID, -v.Amount, domain.StarsReasonBotRefund, v.ChargeID+"-refund", date); err != nil {
			return err
		}
	} else if offer.OwnerID > 0 && offer.OwnerID != offer.BotID {
		if _, err := NewStarsStore(tx).Debit(ctx, offer.OwnerID, v.Amount, domain.StarsReasonBotRefund, domain.Peer{Type: domain.PeerTypeUser, ID: v.PayerID}, date, v.Title, v.Payload); err != nil {
			return err
		}
	} else {
		if _, err := (&StarGiftLifecycleStore{db: tx}).applyBotStarsWalletTx(ctx, tx, domain.BotStarsCredit{BotUserID: v.BotUserID, PayerUserID: v.PayerID, Amount: -v.Amount, Reason: domain.StarsReasonBotRefund, InvoiceKey: v.ChargeID + "-refund", Date: date}); err != nil {
			return err
		}
	}
	checkout, err := sqlcgen.New(tx).GetCommerceCheckout(ctx, v.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	id := checkout.SubscriptionID
	if id == "" {
		id = fmt.Sprintf("botsub-%d", v.ID)
	}
	sub, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).LockCommerceSubscription(ctx, id))
	if errors.Is(err, domain.ErrCommerceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	sub.Canceled = true
	sub.UserCanceled = true
	return putCommerceSubscription(ctx, tx, sub)
}

func (s *BotCommerceStore) CreateSubscriptionInvite(ctx context.Context, botID, channelID, price int64, name string, date int) (domain.ChannelInvite, error) {
	var out domain.ChannelInvite
	if price < 1 || price > 10000 {
		return out, domain.ErrCommerceInvalid
	}
	err := withTx(ctx, s.db, "create subscription invite", func(tx pgx.Tx) error {
		result, err := NewChannelStore(tx).ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: botID, ChannelID: channelID, Title: name, Date: date})
		if err != nil {
			return err
		}
		if !result.Channel.Broadcast {
			return domain.ErrCommerceInvalid
		}
		out = result.Invite
		offer := domain.InvoiceLink{Slug: "subinvite-" + out.Hash, BotID: botID, OwnerID: botID, ChannelID: channelID, InviteHash: out.Hash, Title: "Channel subscription", Description: result.Channel.Title, Payload: out.Hash, Amount: price, SubscriptionPeriod: domain.StarSubscriptionPeriod, Date: date}
		if err := NewBotCommerceStore(tx).SaveSubscriptionInvite(ctx, offer); err != nil {
			return err
		}
		out.SubscriptionPeriod = offer.SubscriptionPeriod
		out.SubscriptionPrice = price
		return nil
	})
	return out, err
}

func requireCommerceInviteEntitlement(ctx context.Context, tx pgx.Tx, invite domain.ChannelInvite, userID int64, date int) error {
	offer, err := NewBotCommerceStore(tx).SubscriptionInvite(ctx, invite.Hash)
	if errors.Is(err, domain.ErrCommerceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	allowed, err := sqlcgen.New(tx).HasCommerceChannelSubscription(ctx, sqlcgen.HasCommerceChannelSubscriptionParams{ChannelID: offer.ChannelID, UserID: userID, Date: int32(date)})
	if err != nil {
		return err
	}
	if !allowed {
		return domain.ErrPaidMediaLocked
	}
	return nil
}
func applyCommercePaymentReceiptTx(ctx context.Context, tx pgx.Tx, invoice domain.BotInvoice, payment *domain.MessagePaymentAction) error {
	checkout, err := sqlcgen.New(tx).GetCommerceCheckout(ctx, invoice.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	offer, err := NewBotCommerceStore(tx).InvoiceLink(ctx, checkout.Slug)
	if err != nil {
		return err
	}
	if offer.SubscriptionPeriod == 0 {
		return nil
	}
	id := checkout.SubscriptionID
	if id == "" {
		id = fmt.Sprintf("botsub-%d", invoice.ID)
	}
	sub, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).GetCommerceSubscription(ctx, id))
	if err != nil {
		return err
	}
	payment.IsRecurring = true
	payment.IsFirstRecurring = checkout.SubscriptionID == ""
	payment.SubscriptionExpirationDate = sub.Until
	return nil
}

func (s *BotCommerceStore) InvoiceOffer(ctx context.Context, invoiceID int64) (domain.InvoiceLink, error) {
	checkout, err := sqlcgen.New(s.db).GetCommerceCheckout(ctx, invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoiceLink{}, domain.ErrCommerceNotFound
	}
	if err != nil {
		return domain.InvoiceLink{}, err
	}
	return s.InvoiceLink(ctx, checkout.Slug)
}

func (s *BotCommerceStore) ChangeUserSubscription(ctx context.Context, userID int64, id string, canceled bool, date int) error {
	return withTx(ctx, s.db, "change user stars subscription", func(tx pgx.Tx) error {
		v, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).LockCommerceSubscription(ctx, id))
		if err != nil {
			return err
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
		return putCommerceSubscription(ctx, tx, v)
	})
}
func (s *BotCommerceStore) FulfillSubscription(ctx context.Context, userID int64, id string, date int) (domain.CreateChannelResult, error) {
	var out domain.CreateChannelResult
	err := withTx(ctx, s.db, "fulfill paid channel subscription", func(tx pgx.Tx) error {
		sub, err := commerceDecode[domain.StarSubscription](sqlcgen.New(tx).LockCommerceSubscription(ctx, id))
		if err != nil {
			return err
		}
		if sub.UserID != userID || !sub.Active || sub.Until <= date || sub.ChannelID <= 0 {
			return domain.ErrCommerceInvalid
		}
		offer, err := NewBotCommerceStore(tx).InvoiceLink(ctx, sub.Slug)
		if err != nil {
			return err
		}
		channels := NewChannelStore(tx)
		channel, err := channels.GetChannelByID(ctx, sub.ChannelID)
		if err != nil {
			return err
		}
		out, err = channels.approveInviteImporterTx(ctx, tx, channel, domain.ChannelInvite{ChannelID: sub.ChannelID, AdminUserID: sub.BotID, Hash: offer.InviteHash}, userID, 0, date)
		if errors.Is(err, domain.ErrUserAlreadyParticipant) {
			out.Channel = channel
			return nil
		}
		return err
	})
	if err == nil {
		out.Recipients, _ = NewChannelStore(s.db).ListActiveChannelMemberIDs(ctx, userID, out.Channel.ID, 0)
	}
	return out, err
}
