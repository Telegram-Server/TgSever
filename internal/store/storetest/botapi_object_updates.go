package storetest

import (
	"context"
	"encoding/json"
	"telesrv/internal/domain"
	"testing"
)

func BotAPIObjectUpdates(t *testing.T, f BotAPIMemberUpdateFixture) {
	t.Helper()
	ctx := context.Background()
	from := map[string]any{"id": f.OtherUserID, "is_bot": false, "first_name": "Caller"}
	payload := map[string]any{"id": "query-1", "from": from, "query": "before", "offset": ""}
	object, err := domain.NewBotAPIObject("inline_query", payload)
	if err != nil {
		t.Fatal(err)
	}
	payload["query"] = "after"
	from["first_name"] = "mutated"
	req := domain.EnqueueBotAPIUpdateRequest{BotUserID: f.BotID, Kind: "inline_query", Date: baseNow(), Object: object}
	first, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created {
		t.Fatalf("enqueue=%+v created=%v err=%v", first, created, err)
	}
	first.Object.Payload[0] = '!'
	req.Object.Payload[0] = '!'
	rows, err := f.Store.ListBotAPIUpdates(ctx, f.BotID, first.ID, 100)
	if err != nil || len(rows) != 1 || rows[0].Object == nil {
		t.Fatalf("read=%+v err=%v", rows, err)
	}
	body, err := rows[0].Object.Object()
	if err != nil || body["query"] != "before" || body["from"].(map[string]any)["first_name"] != "Caller" {
		t.Fatalf("snapshot=%+v err=%v", body, err)
	}
	if rows[0].Object.Kind != "inline_query" || rows[0].Kind != "inline_query" {
		t.Fatalf("kind=%+v", rows[0])
	}
	if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{"business_connection"}); err != nil {
		t.Fatal(err)
	}
	object, _ = domain.NewBotAPIObject("inline_query", map[string]any{"id": "query-2", "from": map[string]any{"id": f.OtherUserID, "is_bot": false, "first_name": "Caller"}, "query": "filtered", "offset": ""})
	req.Object = object
	if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err != nil || created {
		t.Fatalf("filter created=%v err=%v", created, err)
	}
	if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, nil); err != nil {
		t.Fatal(err)
	}
	second, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created || second.ID <= rows[0].ID {
		t.Fatalf("reset=%+v created=%v err=%v", second, created, err)
	}
	tail, err := f.Store.ListTailBotAPIUpdates(ctx, f.BotID, 1, 100)
	if err != nil || len(tail) != 1 || tail[0].ID != second.ID {
		t.Fatalf("tail=%+v err=%v", tail, err)
	}
	if err := f.Store.ConfirmBotAPIUpdates(ctx, f.BotID, second.ID); err != nil {
		t.Fatal(err)
	}
	confirmed, found, err := f.Store.ConfirmedBotAPIUpdateID(ctx, f.BotID)
	if err != nil || !found || confirmed != second.ID {
		t.Fatalf("ack=%d found=%v err=%v", confirmed, found, err)
	}
	pending, err := f.Store.PendingBotAPIUpdateCount(ctx, f.BotID)
	if err != nil || pending != 0 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	req.Object = &domain.BotAPIObject{Kind: "inline_query", Payload: json.RawMessage(`{"id":"secret","from":{"id":1,"is_bot":false,"first_name":"x","session_id":42},"query":"x","offset":""}`)}
	if _, _, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err == nil {
		t.Fatal("private field accepted")
	}
	req.Object = object
	req.Kind = "poll"
	if _, _, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err == nil {
		t.Fatal("mismatched kind accepted")
	}
	req.Kind = "inline_query"
	req.Callback = &domain.BotCallbackQuery{ID: 1}
	if _, _, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err == nil {
		t.Fatal("mixed event accepted")
	}
}
