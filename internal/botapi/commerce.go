package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

type CommerceGateway interface {
	BotAPICreateInvoiceLink(context.Context, int64, string, string, string, string, int64, int) (string, error)
	BotAPIGetMyStarBalance(context.Context, int64) (domain.StarsAmount, error)
	BotAPIGetStarTransactions(context.Context, int64, int, int) ([]domain.BotStarTransaction, []domain.User, error)
	BotAPIEditUserStarSubscription(context.Context, int64, int64, string, bool) (bool, error)
	BotAPICreateChatSubscriptionInviteLink(context.Context, int64, int64, string, int, int64) (domain.ChannelInvite, error)
	BotAPIEditChatSubscriptionInviteLink(context.Context, int64, int64, string, string) (domain.ChannelInvite, error)
	BotAPISendPaidMedia(context.Context, domain.SendPaidMediaRequest) (domain.Message, error)
}

func init() {
	registerMethods(map[string]methodFunc{"createInvoiceLink": (*handler).createInvoiceLink, "getStarTransactions": (*handler).getStarTransactions, "getMyStarBalance": (*handler).getMyStarBalance, "editUserStarSubscription": (*handler).editUserStarSubscription, "createChatSubscriptionInviteLink": (*handler).createChatSubscriptionInviteLink, "editChatSubscriptionInviteLink": (*handler).editChatSubscriptionInviteLink, "sendPaidMedia": (*handler).sendPaidMedia})
	registerErrorMarkers("SUBSCRIPTION_ID_INVALID", "SUBSCRIPTION_PRICING_INVALID", "PAYMENT_FORM_INVALID", "PAID_MEDIA_INVALID", "CHAT_TYPE_UNSUPPORTED", "INVOICE_INVALID", "CURRENCY_INVALID", "INVOICE_AMOUNT_INVALID")
	registerMessageProjection(projectPaidMedia)
}
func (h *handler) commerceRequest(w http.ResponseWriter, r *http.Request) (CommerceGateway, map[string]string, bool) {
	g, ok := h.gateway.(CommerceGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, false
	}
	v, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return nil, nil, false
	}
	return g, v, true
}
func commerceResult(w http.ResponseWriter, result any, err error) {
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}
func commerceInteger(raw string, min, max int64) (int64, error) {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < min || v > max {
		return 0, errors.New("BAD_REQUEST")
	}
	return v, nil
}
func (h *handler) createInvoiceLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.commerceRequest(w, r)
	if !ok {
		return
	}
	if v["currency"] != "XTR" || v["provider_token"] != "" {
		commerceResult(w, nil, errors.New("CURRENCY_INVALID"))
		return
	}
	amount, err := invoiceAmount("", v["prices"])
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	period := int64(0)
	if raw, present := v["subscription_period"]; present {
		period, err = commerceInteger(raw, domain.StarSubscriptionPeriod, domain.StarSubscriptionPeriod)
		if err != nil {
			commerceResult(w, nil, errors.New("SUBSCRIPTION_PRICING_INVALID"))
			return
		}
	}
	link, err := g.BotAPICreateInvoiceLink(r.Context(), botID, v["business_connection_id"], v["title"], v["description"], v["payload"], amount, int(period))
	commerceResult(w, link, err)
}
func (h *handler) getMyStarBalance(w http.ResponseWriter, r *http.Request, botID int64) {
	g, _, ok := h.commerceRequest(w, r)
	if !ok {
		return
	}
	amount, err := g.BotAPIGetMyStarBalance(r.Context(), botID)
	out := map[string]any{"amount": amount.Amount}
	if amount.Nanos != 0 {
		out["nanostar_amount"] = amount.Nanos
	}
	commerceResult(w, out, err)
}
func (h *handler) getStarTransactions(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.commerceRequest(w, r)
	if !ok {
		return
	}
	offset, limit := int64(0), int64(100)
	var err error
	if raw, present := v["offset"]; present {
		offset, err = commerceInteger(raw, 0, 2147483647)
		if err != nil {
			commerceResult(w, nil, err)
			return
		}
	}
	if raw, present := v["limit"]; present {
		limit, err = commerceInteger(raw, 1, 100)
		if err != nil {
			commerceResult(w, nil, err)
			return
		}
	}
	rows, users, err := g.BotAPIGetStarTransactions(r.Context(), botID, int(offset), int(limit))
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	userByID := make(map[int64]domain.User, len(users))
	for _, user := range users {
		userByID[user.ID] = user
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		txn := map[string]any{"id": row.ID, "amount": row.Amount, "date": row.Date}
		if row.Nanos != 0 {
			txn["nanostar_amount"] = row.Nanos
		}
		if row.Reason == domain.StarsTransactionReason("paid_broadcast") {
			txn["receiver"] = map[string]any{"type": "telegram_api", "request_count": 1}
		} else if row.Peer.Type == domain.PeerTypeUser && row.Peer.ID > 0 {
			user, found := userByID[row.Peer.ID]
			if !found {
				user = domain.User{ID: row.Peer.ID, Deleted: true}
			}
			kind := "invoice_payment"
			switch row.Reason {
			case domain.StarsReasonPaidMedia:
				kind = "paid_media_payment"
			case domain.StarsReasonBotSpend:
				kind = "gift_purchase"
			case domain.StarsReasonPremium:
				kind = "premium_purchase"
			case domain.StarsTransactionReason("business_transfer"):
				kind = "business_account_transfer"
			}
			partner := map[string]any{"type": "user", "transaction_type": kind, "user": apiUser(user)}
			if kind == "invoice_payment" {
				partner["invoice_payload"] = row.Payload
				if row.SubscriptionPeriod > 0 {
					partner["subscription_period"] = row.SubscriptionPeriod
				}
			}
			if kind == "paid_media_payment" {
				partner["paid_media_payload"] = row.Payload
			}
			if row.Amount >= 0 {
				txn["source"] = partner
			} else {
				txn["receiver"] = partner
			}
		} else {
			partner := map[string]any{"type": "other"}
			if row.Amount >= 0 {
				txn["source"] = partner
			} else {
				txn["receiver"] = partner
			}
		}
		out = append(out, txn)
	}
	writeAPIOK(w, map[string]any{"transactions": out})
}
func (h *handler) editUserStarSubscription(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.commerceRequest(w, r)
	if !ok {
		return
	}
	id, err := commerceInteger(v["user_id"], 1, 1<<63-1)
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	canceled, err := strconv.ParseBool(v["is_canceled"])
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	result, err := g.BotAPIEditUserStarSubscription(r.Context(), botID, id, v["telegram_payment_charge_id"], canceled)
	commerceResult(w, result, err)
}
func (h *handler) createChatSubscriptionInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.commerceRequest(w, r)
	if !ok {
		return
	}
	chat, err := h.chatIDParam(r.Context(), botID, v["chat_id"])
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	period, err := commerceInteger(v["subscription_period"], domain.StarSubscriptionPeriod, domain.StarSubscriptionPeriod)
	if err != nil {
		commerceResult(w, nil, errors.New("SUBSCRIPTION_PRICING_INVALID"))
		return
	}
	price, err := commerceInteger(v["subscription_price"], 1, 10000)
	if err != nil {
		commerceResult(w, nil, errors.New("SUBSCRIPTION_PRICING_INVALID"))
		return
	}
	invite, err := g.BotAPICreateChatSubscriptionInviteLink(r.Context(), botID, chat, v["name"], int(period), price)
	h.writeChatInvite(w, r, botID, invite, err, false)
}
func (h *handler) editChatSubscriptionInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.commerceRequest(w, r)
	if !ok {
		return
	}
	chat, err := h.chatIDParam(r.Context(), botID, v["chat_id"])
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	invite, err := g.BotAPIEditChatSubscriptionInviteLink(r.Context(), botID, chat, v["invite_link"], v["name"])
	h.writeChatInvite(w, r, botID, invite, err, false)
}
func (h *handler) sendPaidMedia(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gateway.(CommerceGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	common, desc := h.parseSendCommon(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	price, err := commerceInteger(values["star_count"], 1, 25000)
	if err != nil {
		commerceResult(w, nil, errors.New("PAID_MEDIA_INVALID"))
		return
	}
	var raw []struct {
		Type              string `json:"type"`
		Media             string `json:"media"`
		Thumbnail         string `json:"thumbnail"`
		Cover             string `json:"cover"`
		StartTimestamp    int    `json:"start_timestamp"`
		Width             int    `json:"width"`
		Height            int    `json:"height"`
		Duration          int    `json:"duration"`
		SupportsStreaming bool   `json:"supports_streaming"`
	}
	if json.Unmarshal([]byte(values["media"]), &raw) != nil || len(raw) < 1 || len(raw) > 10 {
		commerceResult(w, nil, errors.New("PAID_MEDIA_INVALID"))
		return
	}
	options, err := botAPIMessageOptions(values)
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	req := domain.SendPaidMediaRequest{BotID: botID, ChatID: common.chatID, Price: price, Payload: values["payload"], ReplyMarkup: common.markup, Silent: common.silent, Options: options, ShowCaptionAboveMedia: apiBool(values["show_caption_above_media"])}
	req.Caption, req.Entities, err = botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], 1024, false)
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	for _, item := range raw {
		if item.Type != "photo" && item.Type != "video" {
			commerceResult(w, nil, errors.New("PAID_MEDIA_INVALID"))
			return
		}
		metadata := map[string]string{"width": strconv.Itoa(item.Width), "height": strconv.Itoa(item.Height), "duration": strconv.Itoa(item.Duration), "supports_streaming": strconv.FormatBool(item.SupportsStreaming), "start_timestamp": strconv.Itoa(item.StartTimestamp), "cover": item.Cover}
		if strings.HasPrefix(item.Media, "attach://") {
			metadata["thumbnail"] = item.Thumbnail
		}
		file, ok := botAPIFileInput(item.Media, files, "", metadata)
		if !ok {
			commerceResult(w, nil, errors.New("FILE_ID_INVALID"))
			return
		}
		req.Media = append(req.Media, domain.PaidMediaInput{Kind: item.Type, File: file})
	}
	msg, err := g.BotAPISendPaidMedia(r.Context(), req)
	if err != nil {
		commerceResult(w, nil, err)
		return
	}
	h.writeBotMessages(w, r, botID, []domain.Message{msg}, false)
}
func projectPaidMedia(msg domain.Message, out map[string]any) {
	if msg.Media == nil || msg.Media.PaidMedia == nil {
		return
	}
	bundle := msg.Media.PaidMedia
	items := make([]map[string]any, 0, len(bundle.Items))
	for _, item := range bundle.Items {
		if item == nil {
			continue
		}
		if bundle.Unlocked {
			if item.Photo != nil {
				items = append(items, map[string]any{"type": "photo", "photo": apiPhotoSizes(*item.Photo)})
			} else if item.Document != nil {
				projection := apiDocumentMedia(*item.Document)
				video := projection["video"].(map[string]any)
				if item.VideoCover != nil {
					video["cover"] = apiPhotoSizes(*item.VideoCover)
				}
				if item.VideoTimestamp > 0 {
					video["start_timestamp"] = int(item.VideoTimestamp)
				}
				items = append(items, map[string]any{"type": "video", "video": video})
			}
		} else {
			preview := map[string]any{"type": "preview"}
			if item.Photo != nil && len(item.Photo.Sizes) > 0 {
				last := item.Photo.Sizes[len(item.Photo.Sizes)-1]
				preview["width"] = last.W
				preview["height"] = last.H
			}
			if item.Document != nil {
				for _, a := range item.Document.Attributes {
					if a.Kind == domain.DocAttrVideo {
						preview["width"] = a.W
						preview["height"] = a.H
						preview["duration"] = int(a.Duration)
					}
				}
			}
			items = append(items, preview)
		}
	}
	out["paid_media"] = map[string]any{"star_count": bundle.Price, "paid_media": items}
	if msg.Body != "" {
		if entities, ok := out["entities"]; ok {
			out["caption_entities"] = entities
		}
		delete(out, "text")
		delete(out, "entities")
		out["caption"] = msg.Body
	}
	if msg.Media.InvertMedia {
		out["show_caption_above_media"] = true
	}
}
