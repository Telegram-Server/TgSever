package botapi

import (
	"encoding/json"
	"errors"
	"telesrv/internal/domain"
)

func inlineInvoiceContentFromAPI(raw json.RawMessage) (domain.Invoice, string, error) {
	var p struct {
		Title         string          `json:"title"`
		Description   string          `json:"description"`
		Payload       string          `json:"payload"`
		Currency      string          `json:"currency"`
		ProviderToken string          `json:"provider_token"`
		Prices        json.RawMessage `json:"prices"`
		PhotoURL      string          `json:"photo_url"`
		PhotoSize     int             `json:"photo_size"`
		PhotoWidth    int             `json:"photo_width"`
		PhotoHeight   int             `json:"photo_height"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return domain.Invoice{}, "", errors.New("INVOICE_INVALID")
	}
	if p.Currency != "XTR" || p.ProviderToken != "" {
		return domain.Invoice{}, "", errors.New("CURRENCY_INVALID")
	}
	amount, err := invoiceAmount("", string(p.Prices))
	if err != nil {
		return domain.Invoice{}, "", err
	}
	out := domain.Invoice{Title: p.Title, Description: p.Description, AmountStars: amount}
	if !out.Valid() || len(p.Payload) < 1 || len(p.Payload) > 128 {
		return out, "", errors.New("INVOICE_INVALID")
	}
	if p.PhotoURL != "" {
		if !validInlineResultURL(p.PhotoURL) || p.PhotoSize < 0 || p.PhotoWidth < 0 || p.PhotoHeight < 0 {
			return out, "", errors.New("WEBDOCUMENT_INVALID")
		}
		out.Photo = &domain.BotInlineWebDocument{URL: p.PhotoURL, Size: p.PhotoSize, MimeType: "image/jpeg", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrImageSize, W: p.PhotoWidth, H: p.PhotoHeight}}}
	}
	return out, p.Payload, nil
}
