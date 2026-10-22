package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func tgPaidMedia(v *domain.PaidMedia) tg.MessageMediaClass {
	if v == nil {
		return &tg.MessageMediaEmpty{}
	}
	out := &tg.MessageMediaPaidMedia{StarsAmount: v.Price, ExtendedMedia: make([]tg.MessageExtendedMediaClass, 0, len(v.Items))}
	for _, item := range v.Items {
		if v.Unlocked {
			out.ExtendedMedia = append(out.ExtendedMedia, &tg.MessageExtendedMedia{Media: tgMessageMedia(item)})
			continue
		}
		preview := &tg.MessageExtendedMediaPreview{}
		if item != nil && item.Photo != nil && len(item.Photo.Sizes) > 0 {
			size := item.Photo.Sizes[len(item.Photo.Sizes)-1]
			preview.SetW(size.W)
			preview.SetH(size.H)
		}
		if item != nil && item.Document != nil {
			for _, a := range item.Document.Attributes {
				if a.Kind == domain.DocAttrVideo {
					preview.SetW(a.W)
					preview.SetH(a.H)
					preview.SetVideoDuration(int(a.Duration))
				}
			}
		}
		out.ExtendedMedia = append(out.ExtendedMedia, preview)
	}
	return out
}
func (r *Router) enrichPaidMedia(ctx context.Context, userID int64, media *domain.MessageMedia) *domain.MessageMedia {
	if media == nil || media.PaidMedia == nil {
		return media
	}
	clone := *media
	// Fail closed, including a missing dependency or a storage outage. File ids
	// never come from an untrusted message snapshot, only the entitlement store.
	preview := domain.PaidMediaPreview(*media.PaidMedia)
	clone.PaidMedia = &preview
	if r.deps.BotCommerce != nil {
		if v, err := r.deps.BotCommerce.PaidMedia(ctx, media.PaidMedia.ID, userID); err == nil {
			clone.PaidMedia = &v
		}
	}
	return &clone
}

type paidMediaTarget struct {
	media     domain.PaidMedia
	peer      domain.Peer
	messageID int
}

func (r *Router) commerceInvoiceMessage(ctx context.Context, userID int64, inv *tg.InputInvoiceMessage) (domain.Message, error) {
	if inv == nil || inv.MsgID <= 0 {
		return domain.Message{}, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inv.Peer)
	if err != nil {
		return domain.Message{}, err
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return domain.Message{}, notImplementedErr()
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{inv.MsgID})
		if err != nil {
			return domain.Message{}, err
		}
		for _, msg := range history.Messages {
			if msg.ID == inv.MsgID && !msg.Deleted {
				return botAPIMessageFromChannel(userID, msg), nil
			}
		}
	} else {
		if r.deps.Messages == nil {
			return domain.Message{}, notImplementedErr()
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{inv.MsgID})
		if err != nil {
			return domain.Message{}, err
		}
		for _, msg := range list.Messages {
			if msg.ID == inv.MsgID && msg.Peer == peer && !msg.Deleted {
				return msg, nil
			}
		}
	}
	return domain.Message{}, messageIDInvalidErr()
}
func paidMediaFormID(id string, userID int64) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("paidmedia:%s:%d", id, userID)))
	return int64(binary.BigEndian.Uint64(sum[:8])&0x3fffffffffffffff) | 0x4000000000000000
}
func (r *Router) paidMediaPaymentForm(ctx context.Context, userID int64, target paidMediaTarget) (tg.PaymentsPaymentFormClass, bool, error) {
	if err := r.ensureBotAccess(ctx, target.media.BotID, userID); err != nil {
		return nil, true, err
	}
	if target.media.Unlocked {
		return nil, true, tgerr.New(400, "INVOICE_ALREADY_PAID")
	}
	bot, found, err := r.deps.Users.ByID(ctx, userID, target.media.BotID)
	if err != nil {
		return nil, true, err
	}
	if !found {
		return nil, true, tgerr.New(400, "BOT_INVALID")
	}
	return &tg.PaymentsPaymentFormStars{FormID: paidMediaFormID(target.media.ID, userID), BotID: target.media.BotID, Title: "Paid media", Description: "Unlock media", Invoice: tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: "Paid media", Amount: target.media.Price}}}, Users: tgUsersForViewer(userID, []domain.User{bot})}, true, nil
}
func (r *Router) paidMediaPaymentSettle(ctx context.Context, userID int64, req *tg.PaymentsSendPaymentFormRequest, target paidMediaTarget) (tg.PaymentsPaymentResultClass, bool, error) {
	if err := r.ensureBotAccess(ctx, target.media.BotID, userID); err != nil {
		return nil, true, err
	}
	if r.deps.Stars == nil {
		return nil, true, notImplementedErr()
	}
	if req.FormID != paidMediaFormID(target.media.ID, userID) || req.TipAmount != 0 || req.RequestedInfoID != "" || req.ShippingOptionID != "" {
		return nil, true, tgerr.New(400, "PAYMENT_FORM_INVALID")
	}
	if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
		return nil, true, starsErr(err)
	}
	bought, err := r.deps.BotCommerce.PurchasePaidMedia(ctx, target.media.ID, userID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, true, starsErr(err)
	}
	unlocked, err := r.deps.BotCommerce.PaidMedia(ctx, target.media.ID, userID)
	if err != nil {
		return nil, true, err
	}
	projected := tgPaidMedia(&unlocked).(*tg.MessageMediaPaidMedia)
	if bought && unlocked.RevenueChannelID == 0 && unlocked.Payload != "" {
		if user, found, err := r.deps.Users.ByID(ctx, unlocked.BotID, userID); err == nil && found {
			r.enqueueBotAPIObject(ctx, unlocked.BotID, domain.BotAPIUpdateKind("purchased_paid_media"), map[string]any{"from": botapi.SnapshotUser(user), "paid_media_payload": unlocked.Payload})
		}
	}
	return &tg.PaymentsPaymentResult{Updates: &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageExtendedMedia{Peer: tgPeer(target.peer), MsgID: target.messageID, ExtendedMedia: projected.ExtendedMedia}}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(r.clock.Now().Unix())}}, true, nil
}

func (r *Router) enrichPaidPrivateMessages(ctx context.Context, userID int64, messages []domain.Message) []domain.Message {
	copied := false
	for i, msg := range messages {
		if msg.Media == nil || msg.Media.PaidMedia == nil {
			continue
		}
		if !copied {
			messages = append([]domain.Message(nil), messages...)
			copied = true
		}
		messages[i].Media = r.enrichPaidMedia(ctx, userID, msg.Media)
	}
	return messages
}
func (r *Router) enrichPaidChannelMessages(ctx context.Context, userID int64, messages []domain.ChannelMessage) []domain.ChannelMessage {
	copied := false
	for i, msg := range messages {
		if msg.Media == nil || msg.Media.PaidMedia == nil {
			continue
		}
		if !copied {
			messages = append([]domain.ChannelMessage(nil), messages...)
			copied = true
		}
		messages[i].Media = r.enrichPaidMedia(ctx, userID, msg.Media)
	}
	return messages
}
func (r *Router) enrichPaidChannelEvents(ctx context.Context, userID int64, events []domain.ChannelUpdateEvent) []domain.ChannelUpdateEvent {
	copied := false
	for i, event := range events {
		if event.Message.Media == nil || event.Message.Media.PaidMedia == nil {
			continue
		}
		if !copied {
			events = append([]domain.ChannelUpdateEvent(nil), events...)
			copied = true
		}
		events[i].Message.Media = r.enrichPaidMedia(ctx, userID, event.Message.Media)
	}
	return events
}

func (r *Router) paidExtendedMediaUpdates(ctx context.Context, userID int64, peer domain.Peer, ids []int) (tg.UpdatesClass, error) {
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(r.clock.Now().Unix())}
	if r.deps.BotCommerce == nil {
		return out, nil
	}
	var messages []domain.Message
	if peer.Type == domain.PeerTypeChannel {
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, ids)
		if err != nil {
			return nil, err
		}
		for _, msg := range history.Messages {
			if msg.Media != nil && msg.Media.PaidMedia != nil {
				messages = append(messages, botAPIMessageFromChannel(userID, msg))
			}
		}
	} else {
		list, err := r.deps.Messages.GetMessages(ctx, userID, ids)
		if err != nil {
			return nil, err
		}
		messages = list.Messages
	}
	for _, msg := range messages {
		if msg.Peer != peer || msg.Deleted || msg.Media == nil || msg.Media.PaidMedia == nil {
			continue
		}
		media, err := r.deps.BotCommerce.PaidMedia(ctx, msg.Media.PaidMedia.ID, userID)
		if err != nil {
			return nil, err
		}
		if media.Unlocked {
			out.Updates = append(out.Updates, &tg.UpdateMessageExtendedMedia{Peer: tgPeer(peer), MsgID: msg.ID, ExtendedMedia: tgPaidMedia(&media).(*tg.MessageMediaPaidMedia).ExtendedMedia})
		}
	}
	return out, nil
}
