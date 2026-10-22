package botapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

// usernameChatID 是测试里 @publicchannel 解析出的超级群/频道 chat_id。
const usernameChatID int64 = -1000000000042

// usernameChatRecorder 能把 @publicchannel 解析成 usernameChatID，并记下各方法交给网关的
// chat_id：基础假网关已记录的直接读它的字段，没记录的（编辑、删除、删除临时消息、发票）
// 在这里补记到 seen。它同时充当 InvoiceService。
type usernameChatRecorder struct {
	*fakeChatRefGateway
	seen map[string]int64
}

func newUsernameChatRecorder() *usernameChatRecorder {
	ref := &fakeChatRefGateway{
		fakeBotAPIGateway: &fakeBotAPIGateway{},
		resolved:          map[string]int64{"publicchannel": usernameChatID},
	}
	ref.starGiftResult = true
	ref.ephemeralMessage = domain.EphemeralMessage{
		ID: 4, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 42}, SenderUserID: 1001, ReceiverUserID: 2001,
		Date: 1_900_000_000, Content: domain.EphemeralContent{Message: "ephemeral"},
	}
	return &usernameChatRecorder{fakeChatRefGateway: ref, seen: map[string]int64{}}
}

func (g *usernameChatRecorder) handler() http.Handler {
	return (&handler{bots: &fakeBotAPIBots{profile: testBotProfile}, gateway: g, invoices: g}).routes()
}

func (g *usernameChatRecorder) BotAPIEditMessageTextWithOptions(ctx context.Context, botID, chatID int64, messageID int, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool, options domain.BotAPIMessageOptions) (domain.Message, error) {
	g.seen["editMessageText"] = chatID
	return g.fakeBotAPIGateway.BotAPIEditMessageTextWithOptions(ctx, botID, chatID, messageID, text, entities, setReplyMarkup, replyMarkup, disableWebPagePreview, options)
}

func (g *usernameChatRecorder) BotAPIDeleteMessage(ctx context.Context, botID, chatID int64, messageID int) (bool, error) {
	g.seen["deleteMessage"] = chatID
	return g.fakeBotAPIGateway.BotAPIDeleteMessage(ctx, botID, chatID, messageID)
}

func (g *usernameChatRecorder) BotAPIDeleteEphemeral(ctx context.Context, botID, chatID, receiverID int64, messageID int) (bool, error) {
	g.seen["deleteEphemeralMessage"] = chatID
	return g.fakeBotAPIGateway.BotAPIDeleteEphemeral(ctx, botID, chatID, receiverID, messageID)
}

func (g *usernameChatRecorder) BotAPISendInvoice(_ context.Context, _, chatID int64, _, _, _ string, _ int64, _ bool) (domain.Message, error) {
	g.seen["sendInvoice"] = chatID
	return domain.Message{ID: 1}, nil
}

func (g *usernameChatRecorder) BotAPIRefundStarPayment(context.Context, int64, int64, string) (bool, error) {
	return true, nil
}

// firstEphemeralSendChatID 取第一条临时消息发送的 chat_id；没发出时返回 0。
func (g *usernameChatRecorder) firstEphemeralSendChatID() int64 {
	if len(g.ephemeralSends) == 0 {
		return 0
	}
	return g.ephemeralSends[0].ChatID
}

// 凡是接受 chat_id 的方法都接受 @username，并把解析后的 chat_id 交给网关。
func TestMethodsAcceptUsernameChatID(t *testing.T) {
	ephemeralSend := func(g *usernameChatRecorder) int64 { return g.firstEphemeralSendChatID() }
	for _, tc := range []struct {
		method, body string
		chatID       func(*usernameChatRecorder) int64
	}{
		{"sendMessage", `{"chat_id":"@publicchannel","text":"hi"}`,
			func(g *usernameChatRecorder) int64 { return g.sendChatID }},
		{"sendRichMessage", `{"chat_id":"@publicchannel","rich_message":{"html":"<p>hi</p>"}}`,
			func(g *usernameChatRecorder) int64 {
				if !g.sendRichCalled {
					return 0
				}
				return g.sendChatID
			}},
		{"sendPhoto", `{"chat_id":"@publicchannel","photo":"` + encodeBotAPIFileID("photo:7002:m") + `"}`,
			func(g *usernameChatRecorder) int64 { return g.sendMediaChatID }},
		{"editMessageText", `{"chat_id":"@publicchannel","message_id":7,"text":"edited"}`,
			func(g *usernameChatRecorder) int64 { return g.seen["editMessageText"] }},
		{"deleteMessage", `{"chat_id":"@publicchannel","message_id":7}`,
			func(g *usernameChatRecorder) int64 { return g.seen["deleteMessage"] }},
		{"sendGift", `{"gift_id":"5","chat_id":"@publicchannel","request_id":"r1"}`,
			func(g *usernameChatRecorder) int64 { return g.starGiftChatID }},
		{"sendContact", `{"chat_id":"@publicchannel","ephemeral_message_parameters":{"receiver_user_id":2001},"phone_number":"+100","first_name":"Alice"}`, ephemeralSend},
		{"sendLocation", `{"chat_id":"@publicchannel","ephemeral_message_parameters":{"receiver_user_id":2001},"latitude":1.25,"longitude":2.5}`, ephemeralSend},
		{"sendVenue", `{"chat_id":"@publicchannel","ephemeral_message_parameters":{"receiver_user_id":2001},"latitude":1.25,"longitude":2.5,"title":"Place","address":"Street"}`, ephemeralSend},
		{"editEphemeralMessageText", `{"chat_id":"@publicchannel","receiver_user_id":2001,"ephemeral_message_id":77,"text":"edited"}`,
			func(g *usernameChatRecorder) int64 {
				if len(g.ephemeralEdits) == 0 {
					return 0
				}
				return g.ephemeralEdits[0].ChatID
			}},
		{"deleteEphemeralMessage", `{"chat_id":"@publicchannel","receiver_user_id":2001,"ephemeral_message_id":77}`,
			func(g *usernameChatRecorder) int64 { return g.seen["deleteEphemeralMessage"] }},
		{"sendInvoice", `{"chat_id":"@publicchannel","title":"t","description":"d","payload":"p","currency":"XTR","price":10}`,
			func(g *usernameChatRecorder) int64 { return g.seen["sendInvoice"] }},
	} {
		t.Run(tc.method, func(t *testing.T) {
			gateway := newUsernameChatRecorder()
			rec := callMethod(t, gateway.handler(), tc.method, tc.body)
			if resp := decodeAPIResponse(t, rec); !resp.OK {
				t.Fatalf("%s(@publicchannel) = %s", tc.method, rec.Body.String())
			}
			if got := tc.chatID(gateway); got != usernameChatID {
				t.Fatalf("%s: gateway got chat %d, want %d", tc.method, got, usernameChatID)
			}
		})
	}
}

// 解析失败时报出网关的错误，且不会继续调用业务方法。
func TestSendGiftSurfacesUsernameResolutionErrors(t *testing.T) {
	gateway := newUsernameChatRecorder()
	gateway.resolveErr = errors.New("rpc error code 400: USERNAME_NOT_OCCUPIED")
	rec := callMethod(t, gateway.handler(), "sendGift", `{"gift_id":"5","chat_id":"@ghost"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "USERNAME_NOT_OCCUPIED" {
		t.Fatalf("sendGift(@ghost) = %s, want USERNAME_NOT_OCCUPIED", rec.Body.String())
	}
	if gateway.starGiftCalled {
		t.Fatal("gift was sent although the chat could not be resolved")
	}
}

func TestEphemeralSendKeepsItsErrorForMalformedChatID(t *testing.T) {
	gateway := newUsernameChatRecorder()
	rec := callMethod(t, gateway.handler(), "sendContact", `{"chat_id":"abc","ephemeral_message_parameters":{"receiver_user_id":2001},"phone_number":"+100","first_name":"Alice"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "MEDIA_INVALID" {
		t.Fatalf("sendContact(abc) = %s, want MEDIA_INVALID", rec.Body.String())
	}
}
