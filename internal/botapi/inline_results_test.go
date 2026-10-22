package botapi

import (
	"encoding/json"
	"strings"
	"telesrv/internal/domain"
	"testing"
)

func TestInlineInputContentRejectsAmbiguousMessagesAndKeepsLocationOverride(t *testing.T) {
	if _, err := inlineResultFromAPI(`{"type":"article","id":"ambiguous","title":"Ambiguous","input_message_content":{"message_text":"visible","currency":"XTR","title":"Hidden invoice","description":"Item","payload":"item","prices":[{"label":"Item","amount":1}]}}`); err == nil {
		t.Fatal("accepted two incompatible message contents")
	}
	result, err := inlineResultFromAPI(`{"type":"article","id":"location","title":"Meet here","input_message_content":{"latitude":45.2,"longitude":-75.7}}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "" || result.Media == nil || result.Media.Kind != domain.MessageMediaKindGeo || result.Media.Geo.Lat != 45.2 || result.Media.Geo.Long != -75.7 {
		t.Fatalf("location override=%+v", result)
	}
}

func TestPreparedButtonRequiresOneRequestAndSignedIdentifier(t *testing.T) {
	for _, raw := range []string{
		`{"text":"Missing","request_users":{}}`,
		`{"text":"Overflow","request_chat":{"request_id":2147483648,"chat_is_channel":false}}`,
		`{"text":"Ambiguous","request_managed_bot":{"request_id":1},"request_users":{"request_id":2}}`,
		`{"text":"Plain"}`,
	} {
		if _, err := preparedKeyboardButtonFromAPI(raw); err == nil {
			t.Fatalf("accepted invalid prepared button %s", raw)
		}
	}
	button, err := preparedKeyboardButtonFromAPI(`{"text":"Share users","request_users":{"request_id":-2147483648,"user_is_bot":false,"max_quantity":10}}`)
	if err != nil {
		t.Fatal(err)
	}
	if button.ButtonID != -2147483648 || button.MaxQuantity != 10 || button.PeerFilter == nil || !button.PeerFilter.UserIsBotSet || button.PeerFilter.UserIsBot {
		t.Fatalf("request filter=%+v", button)
	}
}

func TestInlineInvoicePayloadIsPrivateAndAccepts128Bytes(t *testing.T) {
	payload := strings.Repeat("private-order-id", 8)
	input := map[string]any{"type": "article", "id": "invoice", "title": "Buy", "input_message_content": map[string]any{"title": "Item", "description": "Description", "currency": "XTR", "payload": payload, "prices": []any{map[string]any{"label": "Item", "amount": 5}}}}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := inlineResultFromAPI(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.InvoicePayload != payload || result.Media == nil || result.Media.ProductInvoice == nil || result.Media.ProductInvoice.StartParam != "" {
		t.Fatalf("invoice payload separation failed: %+v", result)
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), payload) {
		t.Fatal("private order payload leaked into public/persistable inline result")
	}
}
