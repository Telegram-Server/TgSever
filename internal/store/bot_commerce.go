package store

import (
	"context"
	"telesrv/internal/domain"
)

// BotCommerce owns reusable offers, recurring entitlements and media purchases.
// Settlement, receipts, ledgers and entitlement changes are one atomic mutation.
type BotCommerce interface {
	CreateInvoiceLink(context.Context, domain.InvoiceLink) error
	InvoiceLink(context.Context, string) (domain.InvoiceLink, error)
	CreateInvoiceCheckout(context.Context, string, int64, int) (domain.BotInvoice, error)
	InvoiceCheckout(context.Context, string, int64, int64) (domain.BotInvoice, error)
	InvoiceOffer(context.Context, int64) (domain.InvoiceLink, error)
	EditUserSubscription(context.Context, int64, int64, string, bool) error
	ChangeUserSubscription(context.Context, int64, string, bool, int) error
	FulfillSubscription(context.Context, int64, string, int) (domain.CreateChannelResult, error)
	UserSubscriptions(context.Context, int64) ([]domain.StarSubscription, error)
	ProcessSubscriptions(context.Context, int, int) error
	SaveSubscriptionInvite(context.Context, domain.InvoiceLink) error
	CreateSubscriptionInvite(context.Context, int64, int64, int64, string, int) (domain.ChannelInvite, error)
	SubscriptionInvite(context.Context, string) (domain.InvoiceLink, error)
	SavePaidMedia(context.Context, domain.PaidMedia) error
	PaidMedia(context.Context, string, int64) (domain.PaidMedia, error)
	PurchasePaidMedia(context.Context, string, int64, int) (bool, error)
	CanReadPaidFile(context.Context, int64, string) (bool, error)
	BotStarsBalance(context.Context, int64) (int64, error)
	BotStarsBalanceExact(context.Context, int64) (domain.StarsAmount, error)
	BotStarTransactions(context.Context, int64, int, int) ([]domain.BotStarTransaction, error)
	BusinessTransfer(context.Context, int64, int64, int64, int) error
}
