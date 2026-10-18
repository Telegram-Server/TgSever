package botapi

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// usernameChatID 是测试里 @publicchannel 解析出的超级群/频道 chat_id。
const usernameChatID int64 = -1000000000042

func newUsernameChatGateway() *fakeChatRefGateway {
	return &fakeChatRefGateway{
		fakeBotAPIGateway: &fakeBotAPIGateway{},
		resolved:          map[string]int64{"publicchannel": usernameChatID},
	}
}

func TestSendMessageAcceptsUsernameChatID(t *testing.T) {
	gateway := newUsernameChatGateway()
	rec := callMethod(t, newTestHandler(t, gateway), "sendMessage", `{"chat_id":"@publicchannel","text":"hi"}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("sendMessage(@publicchannel) = %s", rec.Body.String())
	}
	if gateway.sendChatID != usernameChatID {
		t.Fatalf("gateway got chat %d, want %d", gateway.sendChatID, usernameChatID)
	}
}

func TestSendRichMessageAcceptsUsernameChatID(t *testing.T) {
	gateway := newUsernameChatGateway()
	rec := callMethod(t, newTestHandler(t, gateway), "sendRichMessage",
		`{"chat_id":"@publicchannel","rich_message":{"html":"<p>hi</p>"}}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("sendRichMessage(@publicchannel) = %s", rec.Body.String())
	}
	if !gateway.sendRichCalled || gateway.sendChatID != usernameChatID {
		t.Fatalf("gateway got chat %d (called=%v), want %d", gateway.sendChatID, gateway.sendRichCalled, usernameChatID)
	}
}

func TestSendMediaAcceptsUsernameChatID(t *testing.T) {
	gateway := newUsernameChatGateway()
	body := `{"chat_id":"@publicchannel","photo":"` + encodeBotAPIFileID("photo:7002:m") + `"}`
	rec := callMethod(t, newTestHandler(t, gateway), "sendPhoto", body)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("sendPhoto(@publicchannel) = %s", rec.Body.String())
	}
	if !gateway.sendMediaCalled || gateway.sendMediaChatID != usernameChatID {
		t.Fatalf("gateway got chat %d (called=%v), want %d", gateway.sendMediaChatID, gateway.sendMediaCalled, usernameChatID)
	}
}

// chatRecordingGateway 记下编辑/删除收到的 chat_id；基础假网关不记录这两处。
type chatRecordingGateway struct {
	*fakeChatRefGateway
	editChatID   int64
	deleteChatID int64
}

func (g *chatRecordingGateway) BotAPIEditMessageText(ctx context.Context, botID, chatID int64, messageID int, text string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup, disableWebPagePreview bool) (domain.Message, error) {
	g.editChatID = chatID
	return g.fakeBotAPIGateway.BotAPIEditMessageText(ctx, botID, chatID, messageID, text, entities, setReplyMarkup, replyMarkup, disableWebPagePreview)
}

func (g *chatRecordingGateway) BotAPIDeleteMessage(ctx context.Context, botID, chatID int64, messageID int) (bool, error) {
	g.deleteChatID = chatID
	return g.fakeBotAPIGateway.BotAPIDeleteMessage(ctx, botID, chatID, messageID)
}

func TestEditMessageTextAcceptsUsernameChatID(t *testing.T) {
	gateway := &chatRecordingGateway{fakeChatRefGateway: newUsernameChatGateway()}
	rec := callMethod(t, newTestHandler(t, gateway), "editMessageText", `{"chat_id":"@publicchannel","message_id":7,"text":"edited"}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("editMessageText(@publicchannel) = %s", rec.Body.String())
	}
	if gateway.editChatID != usernameChatID {
		t.Fatalf("gateway got chat %d, want %d", gateway.editChatID, usernameChatID)
	}
}

func TestDeleteMessageAcceptsUsernameChatID(t *testing.T) {
	gateway := &chatRecordingGateway{fakeChatRefGateway: newUsernameChatGateway()}
	rec := callMethod(t, newTestHandler(t, gateway), "deleteMessage", `{"chat_id":"@publicchannel","message_id":7}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("deleteMessage(@publicchannel) = %s", rec.Body.String())
	}
	if gateway.deleteChatID != usernameChatID {
		t.Fatalf("gateway got chat %d, want %d", gateway.deleteChatID, usernameChatID)
	}
}

func TestSendGiftAcceptsUsernameChatID(t *testing.T) {
	gateway := newUsernameChatGateway()
	gateway.starGiftResult = true
	rec := callMethod(t, newTestHandler(t, gateway), "sendGift", `{"gift_id":"5","chat_id":"@publicchannel","request_id":"r1"}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("sendGift(@publicchannel) = %s", rec.Body.String())
	}
	if gateway.starGiftChatID != usernameChatID || gateway.starGiftUserID != 0 {
		t.Fatalf("gateway got chat %d user %d, want chat %d", gateway.starGiftChatID, gateway.starGiftUserID, usernameChatID)
	}
}

func TestSendGiftSurfacesUsernameResolutionErrors(t *testing.T) {
	gateway := newUsernameChatGateway()
	gateway.resolveErr = errors.New("rpc error code 400: USERNAME_NOT_OCCUPIED")
	rec := callMethod(t, newTestHandler(t, gateway), "sendGift", `{"gift_id":"5","chat_id":"@ghost"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "USERNAME_NOT_OCCUPIED" {
		t.Fatalf("sendGift(@ghost) = %s, want USERNAME_NOT_OCCUPIED", rec.Body.String())
	}
	if gateway.starGiftCalled {
		t.Fatal("gift was sent although the chat could not be resolved")
	}
}

func newUsernameEphemeralGateway() *fakeChatRefGateway {
	gateway := newUsernameChatGateway()
	gateway.ephemeralMessage = domain.EphemeralMessage{
		ID: 4, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 42}, SenderUserID: 1001, ReceiverUserID: 2001,
		Date: 1_900_000_000, Content: domain.EphemeralContent{Message: "ephemeral"},
	}
	return gateway
}

func TestEphemeralSendsAcceptUsernameChatID(t *testing.T) {
	for _, tc := range []struct{ method, fields string }{
		{"sendContact", `"phone_number":"+100","first_name":"Alice"`},
		{"sendLocation", `"latitude":1.25,"longitude":2.5`},
		{"sendVenue", `"latitude":1.25,"longitude":2.5,"title":"Place","address":"Street"`},
	} {
		gateway := newUsernameEphemeralGateway()
		rec := callMethod(t, newTestHandler(t, gateway), tc.method, `{"chat_id":"@publicchannel","receiver_user_id":2001,`+tc.fields+`}`)
		if resp := decodeAPIResponse(t, rec); !resp.OK {
			t.Fatalf("%s(@publicchannel) = %s", tc.method, rec.Body.String())
		}
		if len(gateway.ephemeralSends) != 1 || gateway.ephemeralSends[0].ChatID != usernameChatID {
			t.Fatalf("%s: gateway sends = %+v, want chat %d", tc.method, gateway.ephemeralSends, usernameChatID)
		}
	}
}

func TestEphemeralSendKeepsItsErrorForMalformedChatID(t *testing.T) {
	gateway := newUsernameEphemeralGateway()
	rec := callMethod(t, newTestHandler(t, gateway), "sendContact", `{"chat_id":"abc","receiver_user_id":2001,"phone_number":"+100","first_name":"Alice"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "MEDIA_INVALID" {
		t.Fatalf("sendContact(abc) = %s, want MEDIA_INVALID", rec.Body.String())
	}
}

func TestEditEphemeralMessageAcceptsUsernameChatID(t *testing.T) {
	gateway := newUsernameEphemeralGateway()
	rec := callMethod(t, newTestHandler(t, gateway), "editEphemeralMessageText",
		`{"chat_id":"@publicchannel","receiver_user_id":2001,"ephemeral_message_id":77,"text":"edited"}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("editEphemeralMessageText(@publicchannel) = %s", rec.Body.String())
	}
	if len(gateway.ephemeralEdits) != 1 || gateway.ephemeralEdits[0].ChatID != usernameChatID {
		t.Fatalf("gateway edits = %+v, want chat %d", gateway.ephemeralEdits, usernameChatID)
	}
}

// deleteChatRecordingGateway 记下 deleteEphemeralMessage 收到的 chat_id。
type deleteChatRecordingGateway struct {
	*fakeChatRefGateway
	chatID int64
}

func (g *deleteChatRecordingGateway) BotAPIDeleteEphemeral(ctx context.Context, botID, chatID, receiverID int64, messageID int) (bool, error) {
	g.chatID = chatID
	return g.fakeBotAPIGateway.BotAPIDeleteEphemeral(ctx, botID, chatID, receiverID, messageID)
}

func TestDeleteEphemeralMessageAcceptsUsernameChatID(t *testing.T) {
	gateway := &deleteChatRecordingGateway{fakeChatRefGateway: newUsernameEphemeralGateway()}
	rec := callMethod(t, newTestHandler(t, gateway), "deleteEphemeralMessage",
		`{"chat_id":"@publicchannel","receiver_user_id":2001,"ephemeral_message_id":77}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("deleteEphemeralMessage(@publicchannel) = %s", rec.Body.String())
	}
	if gateway.chatID != usernameChatID {
		t.Fatalf("gateway got chat %d, want %d", gateway.chatID, usernameChatID)
	}
}

type recordingInvoices struct{ chatID int64 }

func (f *recordingInvoices) BotAPISendInvoice(_ context.Context, _, chatID int64, _, _, _ string, _ int64) (domain.Message, error) {
	f.chatID = chatID
	return domain.Message{ID: 1}, nil
}

func (f *recordingInvoices) BotAPIRefundStarPayment(context.Context, int64, int64, string) (bool, error) {
	return true, nil
}

func TestSendInvoiceAcceptsUsernameChatID(t *testing.T) {
	invoices := &recordingInvoices{}
	h := (&handler{bots: &fakeBotAPIBots{profile: testBotProfile}, gateway: newUsernameChatGateway(), invoices: invoices}).routes()
	rec := callMethod(t, h, "sendInvoice", `{"chat_id":"@publicchannel","title":"t","description":"d","payload":"p","currency":"XTR","price":10}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("sendInvoice(@publicchannel) = %s", rec.Body.String())
	}
	if invoices.chatID != usernameChatID {
		t.Fatalf("invoice service got chat %d, want %d", invoices.chatID, usernameChatID)
	}
}
