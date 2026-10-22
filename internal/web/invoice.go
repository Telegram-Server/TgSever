package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"telesrv/internal/domain"
)

type InvoiceLinkResolver interface {
	InvoiceLink(context.Context, string) (domain.InvoiceLink, error)
}

var invoiceLinkPage = template.Must(template.New("invoice").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title></head><body><main><h1>{{.Title}}</h1><p>{{.Description}}</p><p>{{.Amount}} Stars{{if .Recurring}} every 30 days{{end}}</p><a href="{{.URL}}">Open invoice in Telegram</a></main></body></html>`))

// The landing page contains no buyer identity or payment capability. A native
// authenticated client resolves the slug through payments.getPaymentForm.
func NewInvoiceLinkHandler(resolver InvoiceLinkResolver, appScheme string) http.Handler {
	if appScheme == "" {
		appScheme = "tg"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		slug := strings.TrimPrefix(r.PathValue("username"), "$")
		if slug == "" {
			slug = strings.TrimPrefix(strings.TrimRight(r.URL.Path, "/"), "/$")
		}
		if len(slug) != 48 {
			http.NotFound(w, r)
			return
		}
		for _, c := range slug {
			if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
				http.NotFound(w, r)
				return
			}
		}
		offer, err := resolver.InvoiceLink(r.Context(), slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		deepLink := url.URL{Scheme: appScheme, Host: "invoice", RawQuery: url.Values{"slug": {slug}}.Encode()}
		// appScheme is operator configuration, not request input. html/template does
		// not allow custom URI schemes without this explicit trusted conversion.
		_ = invoiceLinkPage.Execute(w, struct {
			Title, Description string
			Amount             int64
			Recurring          bool
			URL                template.URL
		}{offer.Title, offer.Description, offer.Amount, offer.SubscriptionPeriod > 0, template.URL(deepLink.String())})
	})
}
