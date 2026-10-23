package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// Form ids are generated locally because sendPaymentForm in this layer has no
// provider: they only have to round-trip and stay out of the real namespace.
func TestBotInvoiceFormIDRoundTrips(t *testing.T) {
	for _, id := range []int64{1, 2, 99, 1 << 20} {
		formID := botInvoiceFormID(domain.BotInvoice{ID: id})
		got, ok := botInvoiceFromFormID(formID)
		if !ok || got != id {
			t.Fatalf("form id %d round-tripped to %d, ok=%v", id, got, ok)
		}
	}
	// Anything outside the generated range is refused so a client cannot aim a
	// settlement at an arbitrary form id. base+1 is valid: it is invoice 1.
	for _, formID := range []int64{0, 1, -1, botInvoiceFormIDBase - 1, -botInvoiceFormIDBase} {
		if _, ok := botInvoiceFromFormID(formID); ok {
			t.Fatalf("form id %d was accepted, want refusal", formID)
		}
	}
}
