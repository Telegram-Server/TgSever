package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) prepareInlineInvoice(ctx context.Context, botID int64, invoice *domain.Invoice, payload string) error {
	if !invoice.Valid() {
		return tgerr400("INVOICE_INVALID")
	}
	if r.deps.BotCommerce == nil {
		return internalErr()
	}
	if invoice.Slug == "" {
		if len(payload) < 1 || len(payload) > 128 {
			return tgerr400("INVOICE_INVALID")
		}
		slug, err := commerceToken()
		if err != nil {
			return err
		}
		offer := domain.InvoiceLink{Slug: slug, BotID: botID, OwnerID: botID, Title: invoice.Title, Description: invoice.Description, Payload: payload, Amount: invoice.AmountStars, Date: int(r.clock.Now().Unix())}
		if !offer.Valid() {
			return tgerr400("INVOICE_INVALID")
		}
		if err := r.deps.BotCommerce.CreateInvoiceLink(ctx, offer); err != nil {
			return err
		}
		invoice.Slug = slug
	} else {
		offer, err := r.deps.BotCommerce.InvoiceLink(ctx, invoice.Slug)
		if err != nil {
			return err
		}
		if offer.BotID != botID || offer.Amount != invoice.AmountStars || offer.Title != invoice.Title || offer.Description != invoice.Description {
			return tgerr400("INVOICE_INVALID")
		}
	}
	if invoice.Photo != nil {
		if err := validateInlineWebURL(invoice.Photo.URL, true); err != nil {
			return err
		}
		if invoice.Photo.AccessHash == 0 {
			invoice.Photo.AccessHash = randomNonZeroInt64()
		}
	}
	return nil
}

func inlineSpecialMessage(in domain.BotInlineResult) (tg.BotInlineMessageClass, bool) {
	if !in.RichMessage.IsZero() {
		rich, err := tgRichMessage(in.RichMessage)
		if err != nil {
			return nil, true
		}
		msg := &tg.BotInlineMessageRichMessage{RichMessage: *rich}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			msg.SetReplyMarkup(markup)
		}
		return msg, true
	}
	if in.Media != nil && in.Media.ProductInvoice != nil {
		invoice := in.Media.ProductInvoice
		msg := &tg.BotInlineMessageMediaInvoice{Title: invoice.Title, Description: invoice.Description, Currency: "XTR", TotalAmount: invoice.AmountStars}
		if invoice.Photo != nil {
			msg.SetPhoto(tgInlineWebDocument(*invoice.Photo))
		}
		if markup := tgReplyMarkup(in.ReplyMarkup); markup != nil {
			msg.SetReplyMarkup(markup)
		}
		return msg, true
	}
	return nil, false
}
