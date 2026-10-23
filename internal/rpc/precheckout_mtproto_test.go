package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/iamxvbaba/td/proto"
)

// precheckoutSessions 把推给 bot 的 updateBotPrecheckoutQuery 交给测试，其余推送照常记录。
// bot 拿到的 query_id 只有这一个来源，所以测试按它应答就证明了 MTProto 链路自足。
type precheckoutSessions struct {
	*captureSessions
	botID   int64
	queries chan *tg.UpdateBotPrecheckoutQuery
}

func newPrecheckoutSessions() *precheckoutSessions {
	return &precheckoutSessions{
		captureSessions: &captureSessions{},
		queries:         make(chan *tg.UpdateBotPrecheckoutQuery, 8),
	}
}

func (s *precheckoutSessions) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, typ proto.MessageType, msg tg.UpdatesClass) (int, error) {
	if updates, ok := msg.(*tg.Updates); ok {
		for _, update := range updates.Updates {
			if query, ok := update.(*tg.UpdateBotPrecheckoutQuery); ok && userID == s.botID {
				s.queries <- query
			}
		}
	}
	return s.captureSessions.PushToUserExceptAuthKeySession(ctx, userID, excludeAuthKeyID, excludeSessionID, typ, msg)
}

// newMTProtoInvoiceChain 是只靠 MTProto 推送和 pts 的收款链。
func newMTProtoInvoiceChain(t *testing.T) (*invoiceChain, *precheckoutSessions) {
	t.Helper()
	sessions := newPrecheckoutSessions()
	chain := newInvoiceChainWith(t, invoiceChainOptions{sessions: sessions})
	sessions.botID = chain.botID
	return chain, sessions
}

// answerPushed 等推送到达后，以 bot 身份按推送里的 query_id 应答。
func (c *invoiceChain) answerPushed(sessions *precheckoutSessions, ok bool) {
	select {
	case query := <-sessions.queries:
		req := &tg.MessagesSetBotPrecheckoutResultsRequest{QueryID: query.QueryID}
		if ok {
			req.SetSuccess(true)
		} else {
			req.SetError("нет")
		}
		_, _ = c.router.onMessagesSetBotPrecheckoutResults(WithUserID(context.Background(), c.botID), req)
	case <-time.After(5 * time.Second):
	}
}

func (c *invoiceChain) botState(t *testing.T) *tg.UpdatesState {
	t.Helper()
	got := c.dispatch(t, c.botID, &tg.UpdatesGetStateRequest{})
	state, ok := got.(*tg.UpdatesState)
	if !ok {
		t.Fatalf("updates.getState = %T", got)
	}
	return state
}

// botPaymentSentMe 以 bot 身份从 from 起拉 getDifference，返回其中的 messageActionPaymentSentMe。
func (c *invoiceChain) botPaymentSentMe(t *testing.T, from *tg.UpdatesState) []*tg.MessageActionPaymentSentMe {
	t.Helper()
	var buf bin.Buffer
	req := &tg.UpdatesGetDifferenceRequest{Pts: from.Pts, Date: from.Date, Qts: from.Qts}
	if err := req.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := c.router.Dispatch(WithUserID(context.Background(), c.botID), [8]byte{}, 0, &buf)
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	var messages []tg.MessageClass
	switch diff := got.(type) {
	case *tg.UpdatesDifference:
		messages = diff.NewMessages
	case *tg.UpdatesDifferenceSlice:
		messages = diff.NewMessages
	case *tg.UpdatesDifferenceEmpty:
	default:
		t.Fatalf("getDifference = %T", got)
	}
	var out []*tg.MessageActionPaymentSentMe
	for _, m := range messages {
		service, ok := m.(*tg.MessageService)
		if !ok {
			continue
		}
		if action, ok := service.Action.(*tg.MessageActionPaymentSentMe); ok {
			out = append(out, action)
		}
	}
	return out
}

// pre-checkout 在线推给 bot，bot 应答后付款完成，支付结果作为带 pts 的服务消息送达 bot。
func TestPreCheckoutOverMTProto(t *testing.T) {
	t.Run("answered pays and the bot sees the payment by pts", func(t *testing.T) {
		chain, sessions := newMTProtoInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "mtproto-ok", 100)
		before := chain.botState(t)

		go chain.answerPushed(sessions, true)
		if err := chain.payStarsForm(t, invoice, stored.ID); err != nil {
			t.Fatalf("payment over MTProto: %v", err)
		}
		if chain.walletBalance(t) != 100 {
			t.Fatalf("bot wallet = %d, want 100", chain.walletBalance(t))
		}
		paid := chain.botPaymentSentMe(t, before)
		if len(paid) != 1 {
			t.Fatalf("payment service messages by pts = %d, want 1", len(paid))
		}
		if paid[0].TotalAmount != 100 || string(paid[0].Payload) != "mtproto-ok" || paid[0].Charge.ID == "" {
			t.Fatalf("payment action = %+v, want 100 XTR with the payload and a charge id", paid[0])
		}
	})

	t.Run("declined fails the payment", func(t *testing.T) {
		chain, sessions := newMTProtoInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "mtproto-no", 100)
		go chain.answerPushed(sessions, false)
		err := chain.payStarsForm(t, invoice, stored.ID)
		if code, ok := tgerr.As(err); !ok || code.Type != "PAYMENT_FAILED" {
			t.Fatalf("declined payment error = %v, want PAYMENT_FAILED", err)
		}
		if chain.walletBalance(t) != 0 {
			t.Fatalf("bot wallet = %d after a decline, want 0", chain.walletBalance(t))
		}
	})

	t.Run("silence fails the payment after ten seconds", func(t *testing.T) {
		chain, sessions := newMTProtoInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "mtproto-silent", 100)
		start := time.Now()
		err := chain.payStarsForm(t, invoice, stored.ID)
		elapsed := time.Since(start)
		if code, ok := tgerr.As(err); !ok || code.Code != 400 {
			t.Fatalf("timeout error = %v, want a 400 rpc_error", err)
		}
		if elapsed < preCheckoutTimeout {
			t.Fatalf("gave up after %v, want the full %v", elapsed, preCheckoutTimeout)
		}
		select {
		case <-sessions.queries:
		default:
			t.Fatal("the bot was never asked")
		}
		if chain.walletBalance(t) != 0 {
			t.Fatalf("bot wallet = %d after a timeout, want 0", chain.walletBalance(t))
		}
	})
}
