package rpc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appbots "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appfiles "telesrv/internal/app/files"
	appmessages "telesrv/internal/app/messages"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type commerceRPCFixture struct {
	r          *Router
	buyer, bot domain.User
	commerce   *memory.BotCommerceStore
	messages   *memory.MessageStore
}

func newCommerceRPCFixture(t *testing.T) commerceRPCFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	buyer, err := users.Create(ctx, domain.User{Phone: "15558881234", FirstName: "Buyer", AccessHash: 123})
	if err != nil {
		t.Fatal(err)
	}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := memory.NewBotStore(users)
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 234, FirstName: "Commerce", Username: "CommerceBot"}, domain.BotProfile{OwnerUserID: buyer.ID, TokenSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	stars := memory.NewStarsStore()
	if _, err = stars.Credit(ctx, buyer.ID, 100, domain.StarsReasonTopup, domain.Peer{}, int(time.Now().Unix()), "Funding", ""); err != nil {
		t.Fatal(err)
	}
	updates := memory.NewBotAPIUpdateStore()
	channels := memory.NewChannelStore()
	commerce := memory.NewBotCommerceStore(stars, bots, messages, channels, updates)
	blobs, err := appfiles.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{PublicBaseURL: "https://links.example.test"}, Deps{Users: appusers.NewService(users), Bots: appbots.NewService(users, bots, messages), Messages: appmessages.NewService(messages, dialogs), Channels: appchannels.NewService(channels), Stars: appstars.NewService(stars, appstars.WithStartingGrant(0)), Files: appfiles.NewService(memory.NewMediaStore(), blobs, 2), BotCommerce: commerce, BotBroadcast: commerce, BotAPIUpdates: updates}, zaptest.NewLogger(t), clock.System)
	return commerceRPCFixture{r, buyer, bot, commerce, messages}
}
func TestPaidMediaNativeCheckoutAndDownloadAreViewerGated(t *testing.T) {
	f := newCommerceRPCFixture(t)
	ctx := context.Background()
	buyerCtx := WithUserID(ctx, f.buyer.ID)
	picture := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			picture.Set(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	sent, err := f.r.BotAPISendPaidMedia(ctx, domain.SendPaidMediaRequest{BotID: f.bot.ID, ChatID: f.buyer.ID, Price: 40, Payload: "paid-photo-order", Media: []domain.PaidMediaInput{{Kind: "photo", File: domain.BotAPIFileInput{Bytes: encoded.Bytes(), MimeType: "image/png", FileName: "red.png"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Media == nil || sent.Media.PaidMedia == nil || !sent.Media.PaidMedia.Unlocked {
		t.Fatalf("seller paid media=%+v", sent.Media)
	}
	photo := sent.Media.PaidMedia.Items[0].Photo
	var size domain.PhotoSize
	for _, candidate := range photo.Sizes {
		if candidate.Downloadable() {
			size = candidate
		}
	}
	if size.Type == "" {
		t.Fatal("uploaded photo has no downloadable rendition")
	}
	loc := &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size.Type}
	if _, err = f.r.onUploadGetFile(buyerCtx, &tg.UploadGetFileRequest{Location: loc, Limit: 65536}); err == nil {
		t.Fatal("unpaid user downloaded real file")
	}
	peer := &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	invoice := &tg.InputInvoiceMessage{Peer: peer, MsgID: 1}
	before, err := f.r.onMessagesGetExtendedMedia(buyerCtx, &tg.MessagesGetExtendedMediaRequest{Peer: peer, ID: []int{1}})
	if err != nil || len(before.(*tg.Updates).Updates) != 0 {
		t.Fatalf("unpaid refresh=%+v %v", before, err)
	}
	form, err := f.r.onPaymentsGetPaymentForm(buyerCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatal(err)
	}
	req := &tg.PaymentsSendStarsFormRequest{Invoice: invoice, FormID: form.(*tg.PaymentsPaymentFormStars).FormID}
	for range 2 {
		if _, err = f.r.onPaymentsSendStarsForm(buyerCtx, req); err != nil {
			t.Fatal(err)
		}
	}
	balance, err := f.r.deps.Stars.GetBalance(ctx, f.buyer.ID)
	if err != nil || balance.Balance != 60 {
		t.Fatalf("buyer balance=%+v %v", balance, err)
	}
	wallet, err := f.r.BotAPIGetMyStarBalance(ctx, f.bot.ID)
	if err != nil || wallet.Amount != 40 {
		t.Fatalf("proceeds=%+v %v", wallet, err)
	}
	downloaded, err := f.r.onUploadGetFile(buyerCtx, &tg.UploadGetFileRequest{Location: loc, Limit: 65536})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(downloaded.(*tg.UploadFile).Bytes))
	if err != nil || decoded.Bounds().Dx() != 8 || decoded.Bounds().Dy() != 8 {
		t.Fatalf("downloaded image=%v %v", decoded, err)
	}
	after, err := f.r.onMessagesGetExtendedMedia(buyerCtx, &tg.MessagesGetExtendedMediaRequest{Peer: peer, ID: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	updates := after.(*tg.Updates).Updates
	if len(updates) != 1 {
		t.Fatalf("refresh=%+v", updates)
	}
	revealed := updates[0].(*tg.UpdateMessageExtendedMedia).ExtendedMedia[0].(*tg.MessageExtendedMedia)
	if revealed.Media.(*tg.MessageMediaPhoto).Photo.(*tg.Photo).ID != photo.ID {
		t.Fatal("refresh revealed different photo")
	}
}
func TestInlineInvoiceLinkCheckoutUsesPersistedOffer(t *testing.T) {
	f := newCommerceRPCFixture(t)
	ctx, cancel := context.WithTimeout(WithUserID(context.Background(), f.buyer.ID), 5*time.Second)
	defer cancel()
	link, err := f.r.BotAPICreateInvoiceLink(ctx, f.bot.ID, "", "Inline access", "Private paid service", "inline-order", 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, slug, ok := strings.Cut(link, "/$")
	if !ok {
		t.Fatalf("invoice link=%q", link)
	}
	source, err := f.r.deps.Messages.SendPrivateText(ctx, f.buyer.ID, domain.SendPrivateTextRequest{SenderUserID: f.buyer.ID, RecipientUserID: f.buyer.ID, RandomID: 987654, ViaBotID: f.bot.ID, Date: int(time.Now().Unix()), Media: &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice, ProductInvoice: &domain.Invoice{Title: "Inline access", Description: "Private paid service", AmountStars: 40, Slug: slug}}})
	if err != nil {
		t.Fatal(err)
	}
	invoice := &tg.InputInvoiceMessage{Peer: &tg.InputPeerSelf{}, MsgID: source.SenderMessage.ID}
	form, err := f.r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatal(err)
	}
	beforePayment, err := f.r.deps.Messages.GetMessages(ctx, f.buyer.ID, []int{1, 2, 3})
	if err != nil || len(beforePayment.Messages) != 1 {
		t.Fatalf("opening a link emitted invoice messages: %+v %v", beforePayment.Messages, err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{Invoice: invoice, FormID: form.(*tg.PaymentsPaymentFormStars).FormID})
		result <- err
	}()
	var query int64
	for query == 0 {
		pending := f.r.preCheckouts.pendingSnapshot()
		if len(pending) > 0 {
			query = pending[0]
			break
		}
		select {
		case err := <-result:
			t.Fatalf("payment completed before checkout answer: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	balance, _ := f.r.deps.Stars.GetBalance(ctx, f.buyer.ID)
	if balance.Balance != 100 {
		t.Fatal("precheckout debited funds before bot approval")
	}
	accepted, err := f.r.BotAPIAnswerPreCheckoutQuery(ctx, f.bot.ID, strconv.FormatInt(query, 10), true, "")
	if err != nil || !accepted {
		t.Fatalf("answer=%v %v", accepted, err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	balance, _ = f.r.deps.Stars.GetBalance(ctx, f.buyer.ID)
	if balance.Balance != 60 {
		t.Fatalf("inline invoice debit=%d", balance.Balance)
	}
	rows, _, err := f.r.BotAPIGetStarTransactions(ctx, f.bot.ID, 0, 100)
	if err != nil || len(rows) != 1 || rows[0].Amount != 40 || rows[0].Payload != "inline-order" {
		t.Fatalf("inline proceeds=%+v %v", rows, err)
	}
}
