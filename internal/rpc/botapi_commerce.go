package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
)

func commerceToken() (string, error) {
	var token [24]byte
	_, err := rand.Read(token[:])
	return hex.EncodeToString(token[:]), err
}
func (r *Router) ProcessBotSubscriptions(ctx context.Context, date, limit int) error {
	if r.deps.BotCommerce == nil {
		return nil
	}
	return r.deps.BotCommerce.ProcessSubscriptions(ctx, date, limit)
}
func (r *Router) BotAPICreateInvoiceLink(ctx context.Context, botID int64, connection, title, description, payload string, amount int64, period int) (string, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return "", err
	}
	if r.deps.BotCommerce == nil {
		return "", notImplementedErr()
	}
	owner := botID
	if connection != "" {
		var err error
		owner, err = r.botAPIBusinessOwner(ctx, botID, connection, "can_reply")
		if err != nil {
			return "", err
		}
	}
	slug, err := commerceToken()
	if err != nil {
		return "", err
	}
	offer := domain.InvoiceLink{Slug: slug, BotID: botID, OwnerID: owner, BusinessConnectionID: connection, Title: title, Description: description, Payload: payload, Amount: amount, SubscriptionPeriod: period, Date: int(r.clock.Now().Unix())}
	if !offer.Valid() {
		return "", tgerr.New(400, "INVOICE_INVALID")
	}
	if err := r.deps.BotCommerce.CreateInvoiceLink(ctx, offer); err != nil {
		return "", err
	}
	return publicLinkWithBaseURL(r.cfg.PublicBaseURL, "$"+slug), nil
}
func (r *Router) BotAPIGetMyStarBalance(ctx context.Context, botID int64) (domain.StarsAmount, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.StarsAmount{}, err
	}
	if r.deps.BotCommerce == nil {
		return domain.StarsAmount{}, notImplementedErr()
	}
	return r.deps.BotCommerce.BotStarsBalanceExact(ctx, botID)
}
func (r *Router) BotAPIGetStarTransactions(ctx context.Context, botID int64, offset, limit int) ([]domain.BotStarTransaction, []domain.User, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return nil, nil, err
	}
	if r.deps.BotCommerce == nil {
		return nil, nil, notImplementedErr()
	}
	rows, err := r.deps.BotCommerce.BotStarTransactions(ctx, botID, offset, limit)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(rows))
	seen := make(map[int64]bool)
	for _, row := range rows {
		if row.Peer.Type == domain.PeerTypeUser && !seen[row.Peer.ID] {
			seen[row.Peer.ID] = true
			ids = append(ids, row.Peer.ID)
		}
	}
	users, err := r.deps.Users.ByIDs(ctx, botID, ids)
	return rows, users, err
}
func (r *Router) BotAPIEditUserStarSubscription(ctx context.Context, botID, userID int64, charge string, canceled bool) (bool, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return false, err
	}
	if r.deps.BotCommerce == nil {
		return false, notImplementedErr()
	}
	if userID <= 0 || charge == "" {
		return false, tgerr.New(400, "SUBSCRIPTION_ID_INVALID")
	}
	err := r.deps.BotCommerce.EditUserSubscription(ctx, botID, userID, charge, canceled)
	if errors.Is(err, domain.ErrCommerceNotFound) || errors.Is(err, domain.ErrCommerceInvalid) {
		return false, tgerr.New(400, "SUBSCRIPTION_ID_INVALID")
	}
	return err == nil, err
}
func (r *Router) BotAPICreateChatSubscriptionInviteLink(ctx context.Context, botID, chatID int64, name string, period int, price int64) (domain.ChannelInvite, error) {
	if r.deps.BotCommerce == nil {
		return domain.ChannelInvite{}, notImplementedErr()
	}
	if period != domain.StarSubscriptionPeriod || price < 1 || price > 10000 {
		return domain.ChannelInvite{}, tgerr.New(400, "SUBSCRIPTION_PRICING_INVALID")
	}
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	invite, err := r.deps.BotCommerce.CreateSubscriptionInvite(ctx, botID, peer.ChannelID, price, name, int(r.clock.Now().Unix()))
	if err != nil {
		return invite, channelInviteErr(err)
	}
	invite.Link = publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+"+invite.Hash)
	return invite, nil
}
func (r *Router) BotAPIEditChatSubscriptionInviteLink(ctx context.Context, botID, chatID int64, link, name string) (domain.ChannelInvite, error) {
	if r.deps.BotCommerce == nil {
		return domain.ChannelInvite{}, notImplementedErr()
	}
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	hash, err := channelInviteHashFromLink(link)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	offer, err := r.deps.BotCommerce.SubscriptionInvite(ctx, hash)
	if err != nil || offer.BotID != botID || offer.ChannelID != peer.ChannelID {
		return domain.ChannelInvite{}, tgerr.New(400, "INVITE_HASH_INVALID")
	}
	result, err := r.deps.Channels.EditExportedInvite(ctx, botID, domain.EditChannelInviteRequest{UserID: botID, ChannelID: peer.ChannelID, Hash: hash, HasTitle: true, Title: name, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return domain.ChannelInvite{}, channelInviteErr(err)
	}
	result.Invite.SubscriptionPeriod = offer.SubscriptionPeriod
	result.Invite.SubscriptionPrice = offer.Amount
	result.Invite.Link = publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+"+hash)
	return result.Invite, nil
}
func (r *Router) BotAPISendPaidMedia(ctx context.Context, req domain.SendPaidMediaRequest) (domain.Message, error) {
	if _, err := r.BotAPISelf(ctx, req.BotID); err != nil {
		return domain.Message{}, err
	}
	if r.deps.BotCommerce == nil {
		return domain.Message{}, notImplementedErr()
	}
	if req.Price < 1 || req.Price > 25000 || len(req.Media) < 1 || len(req.Media) > 10 || len(req.Payload) > 128 || utf8.RuneCountInString(req.Caption) > 1024 {
		return domain.Message{}, tgerr.New(400, "MEDIA_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(req.ChatID)
	if !ok {
		return domain.Message{}, tgerr.New(400, "CHAT_ID_INVALID")
	}
	actor := req.BotID
	if req.Options.BusinessConnectionID != "" {
		var err error
		actor, err = r.botAPIBusinessPeer(ctx, req.BotID, req.Options.BusinessConnectionID, "can_reply", peer)
		if err != nil {
			return domain.Message{}, err
		}
	}
	revenueChannelID := int64(0)
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return domain.Message{}, notImplementedErr()
		}
		view, err := r.deps.Channels.GetChannel(ctx, actor, peer.ID)
		if err != nil {
			return domain.Message{}, err
		}
		if view.Channel.Broadcast {
			revenueChannelID = peer.ID
		}
	}
	id, err := commerceToken()
	if err != nil {
		return domain.Message{}, err
	}
	bundle := domain.PaidMedia{ID: id, BotID: req.BotID, OwnerID: actor, ChatID: peer.ID, RevenueChannelID: revenueChannelID, Price: req.Price, Payload: req.Payload}
	if peer.Type == domain.PeerTypeChannel {
		bundle.ChannelID = peer.ID
	}
	for _, input := range req.Media {
		if input.Kind != "photo" && input.Kind != "video" {
			return domain.Message{}, tgerr.New(400, "MEDIA_INVALID")
		}
		media, err := r.botAPIEphemeralMedia(ctx, actor, input.Kind, input.File, domain.BotAPIFileInput{})
		if err != nil {
			return domain.Message{}, err
		}
		bundle.Items = append(bundle.Items, media)
	}
	if err := r.deps.BotCommerce.SavePaidMedia(ctx, bundle); err != nil {
		return domain.Message{}, err
	}
	preview := domain.PaidMediaPreview(bundle)
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindPaidMedia, PaidMedia: &preview, InvertMedia: req.ShowCaptionAboveMedia}
	replyID := 0
	if req.Options.ReplyParameters != nil {
		replyID = req.Options.ReplyParameters.MessageID
	}
	msg, err := r.botAPISendContent(ctx, req.BotID, req.ChatID, req.Caption, req.Entities, media, nil, req.ReplyMarkup, req.Silent, req.Options.ProtectContent, replyID, 0, req.Options)
	msg.Media = r.enrichPaidMedia(ctx, actor, msg.Media)
	return msg, err
}

func (r *Router) commerceInvoiceTarget(ctx context.Context, userID int64, input tg.InputInvoiceClass) (string, *paidMediaTarget, bool, error) {
	switch inv := input.(type) {
	case *tg.InputInvoiceSlug:
		if inv == nil || inv.Slug == "" {
			return "", nil, true, tgerr.New(400, "INVOICE_INVALID")
		}
		return inv.Slug, nil, true, nil
	case *tg.InputInvoiceChatInviteSubscription:
		if inv == nil || r.deps.BotCommerce == nil {
			return "", nil, true, tgerr.New(400, "INVOICE_INVALID")
		}
		offer, err := r.deps.BotCommerce.SubscriptionInvite(ctx, inv.Hash)
		return offer.Slug, nil, true, err
	case *tg.InputInvoiceMessage:
		msg, err := r.commerceInvoiceMessage(ctx, userID, inv)
		if err != nil {
			return "", nil, true, err
		}
		if msg.Media == nil {
			return "", nil, false, nil
		}
		if invoice := msg.Media.ProductInvoice; invoice != nil && invoice.Slug != "" {
			offer, err := r.deps.BotCommerce.InvoiceLink(ctx, invoice.Slug)
			if err != nil {
				return "", nil, true, commerceError(err)
			}
			if msg.ViaBotID != offer.BotID && !(msg.From.Type == domain.PeerTypeUser && msg.From.ID == offer.BotID) {
				return "", nil, true, tgerr.New(400, "INVOICE_INVALID")
			}
			return offer.Slug, nil, true, nil
		}
		if media := msg.Media.PaidMedia; media != nil {
			bundle, err := r.deps.BotCommerce.PaidMedia(ctx, media.ID, userID)
			return "", &paidMediaTarget{media: bundle, peer: msg.Peer, messageID: msg.ID}, true, err
		}
	}
	return "", nil, false, nil
}
func (r *Router) commercePaymentForm(ctx context.Context, userID int64, input tg.InputInvoiceClass) (tg.PaymentsPaymentFormClass, bool, error) {
	if r.deps.BotCommerce == nil {
		return nil, false, nil
	}
	slug, paid, handled, err := r.commerceInvoiceTarget(ctx, userID, input)
	if err != nil {
		return nil, handled, err
	}
	if paid != nil {
		return r.paidMediaPaymentForm(ctx, userID, *paid)
	}
	if !handled {
		return nil, false, nil
	}
	offer, err := r.deps.BotCommerce.InvoiceLink(ctx, slug)
	if err != nil {
		return nil, true, tgerr.New(400, "INVOICE_INVALID")
	}
	if err := r.ensureBotAccess(ctx, offer.BotID, userID); err != nil {
		return nil, true, err
	}
	if offer.BusinessConnectionID != "" {
		if _, err := r.botAPIBusinessOwner(ctx, offer.BotID, offer.BusinessConnectionID, "can_reply"); err != nil {
			return nil, true, err
		}
	}
	invoice, err := r.deps.BotCommerce.CreateInvoiceCheckout(ctx, slug, userID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, true, err
	}
	bot, found, err := r.deps.Users.ByID(ctx, userID, invoice.BotUserID)
	if err != nil {
		return nil, true, err
	}
	if !found {
		return nil, true, tgerr.New(400, "INVOICE_INVALID")
	}
	form := &tg.PaymentsPaymentFormStars{FormID: botInvoiceFormID(invoice), BotID: invoice.BotUserID, Title: invoice.Title, Description: invoice.Description, Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: invoice.Title, Amount: invoice.Amount}}}, Users: tgUsersForViewer(userID, []domain.User{bot})}
	if offer.SubscriptionPeriod > 0 {
		form.Invoice.SetSubscriptionPeriod(offer.SubscriptionPeriod)
	}
	return form, true, nil
}
func (r *Router) commercePaymentSettle(ctx context.Context, userID int64, req *tg.PaymentsSendPaymentFormRequest) (tg.PaymentsPaymentResultClass, bool, error) {
	if r.deps.BotCommerce == nil {
		return nil, false, nil
	}
	slug, paid, handled, err := r.commerceInvoiceTarget(ctx, userID, req.Invoice)
	if err != nil {
		return nil, handled, err
	}
	if paid != nil {
		return r.paidMediaPaymentSettle(ctx, userID, req, *paid)
	}
	if !handled {
		return nil, false, nil
	}
	id, ok := botInvoiceFromFormID(req.FormID)
	if !ok {
		return nil, true, tgerr.New(400, "PAYMENT_FORM_INVALID")
	}
	invoice, err := r.deps.BotCommerce.InvoiceCheckout(ctx, slug, userID, id)
	if err != nil {
		return nil, true, tgerr.New(400, "PAYMENT_FORM_INVALID")
	}
	return r.settleBotInvoice(ctx, userID, req, invoice)
}
func commerceError(err error) error {
	if errors.Is(err, domain.ErrCommerceNotFound) || errors.Is(err, domain.ErrCommerceInvalid) {
		return tgerr.New(400, "INVOICE_INVALID")
	}
	return err
}
