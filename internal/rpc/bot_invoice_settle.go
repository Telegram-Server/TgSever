package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/store"
)

// botInvoiceSettle commits through a single durable payment aggregate.
func (r *Router) botInvoiceSettle(ctx context.Context, userID int64, req *tg.PaymentsSendPaymentFormRequest, inv *tg.InputInvoiceMessage) (tg.PaymentsPaymentResultClass, bool, error) {
	if r.deps.Bots == nil || r.deps.Stars == nil {
		return nil, false, notImplementedErr()
	}
	invoice, err := r.botInvoiceForMessage(ctx, userID, inv.Peer, inv.MsgID)
	if err != nil {
		if isInvoiceLookupMiss(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	// A real XTR purchase carries neither shipping nor a tip: telesrv models no
	// fiat checkout, so anything extra is a request it cannot honour.
	if req.RequestedInfoID != "" {
		return nil, true, tgerr.New(400, "REQUESTED_INFO_ID_INVALID")
	}
	if req.ShippingOptionID != "" {
		return nil, true, tgerr.New(400, "SHIPPING_OPTION_INVALID")
	}
	if req.TipAmount != 0 {
		return nil, true, tgerr.New(400, "TIP_AMOUNT_INVALID")
	}
	if req.FormID != botInvoiceFormID(invoice) {
		return nil, true, tgerr.New(400, "PAYMENT_FORM_INVALID")
	}
	payments, ok := r.deps.Bots.(store.BotInvoicePayments)
	if !ok {
		return nil, true, notImplementedErr()
	}

	if invoice.Refunded {
		return nil, true, tgerr.New(400, "INVOICE_REFUNDED")
	}
	// Pre-checkout runs before anything is marked, not after. Settling first would
	// leave the invoice flagged as paid even when the bot refuses, and the retry
	// would then return a receipt with no money behind it.
	//
	// An already settled invoice skips the question: the order was approved once
	// and a replay must not re-ask or re-charge.
	if !invoice.Settled() {
		if err := r.runPreCheckout(ctx, invoice.BotUserID, userID,
			invoice.Currency, invoice.Amount, invoice.Payload); err != nil {
			return nil, true, err
		}
	}
	// Apply the configured starting grant before checkout, exactly as the Stars service does.
	if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
		return nil, true, starsErr(err)
	}
	sent, err := payments.PayBotInvoice(ctx, invoice.ID, userID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, true, starsErr(err)
	}
	return &tg.PaymentsPaymentResult{Updates: tgPrivateSendResultUpdates(sent, 0, false, nil, nil)}, true, nil
}
