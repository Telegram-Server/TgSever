package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestInvoiceLinkLandingResolvesStoredOfferAndNativeCheckout(t *testing.T) {
	users := memory.NewUserStore()
	commerce := memory.NewBotCommerceStore(memory.NewStarsStore(), memory.NewBotStore(users), memory.NewMessageStore(), memory.NewChannelStore())
	slug := strings.Repeat("a1", 24)
	if err := commerce.CreateInvoiceLink(context.Background(), domain.InvoiceLink{Slug: slug, BotID: 1, OwnerID: 1, Title: "<script>unsafe</script>", Description: "Monthly access", Payload: "private-order-token", Amount: 42, SubscriptionPeriod: domain.StarSubscriptionPeriod, Date: 1800000000}); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://links.example.test", "https://links.example.test/public/v2/"} {
		h, err := NewHandler(Config{StickerSets: fakeResolver{}, PublicBaseURL: base, InvoiceLinks: NewInvoiceLinkHandler(commerce, "owpg")})
		if err != nil {
			t.Fatal(err)
		}
		prefix := ""
		if strings.Contains(base, "/public/") {
			prefix = "/public/v2"
		}
		for _, suffix := range []string{"", "/"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, prefix+"/$"+slug+suffix, nil))
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, "owpg://invoice?slug="+slug) || !strings.Contains(body, "42 Stars") || !strings.Contains(body, "every 30 days") {
				t.Fatalf("landing status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(body, "<script>") || strings.Contains(body, "private-order-token") {
				t.Fatal("landing leaked executable title or private payload")
			}
		}
		missing := httptest.NewRecorder()
		h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, prefix+"/$"+strings.Repeat("b2", 24), nil))
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing status=%d", missing.Code)
		}
	}
}
