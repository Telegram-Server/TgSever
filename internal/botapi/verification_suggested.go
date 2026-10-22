package botapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

type VerificationGateway interface {
	BotAPISetVerification(ctx context.Context, botID, chatID int64, enabled bool, description string) error
	BotAPIResolveVerificationChatUsername(ctx context.Context, botID int64, username string) (int64, error)
}

type SuggestedPostGateway interface {
	BotAPIToggleSuggestedPost(ctx context.Context, botID, chatID int64, messageID int, reject bool, comment string, sendDate int) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"verifyUser":             (*handler).verifyUser,
		"verifyChat":             (*handler).verifyChat,
		"removeUserVerification": (*handler).removeUserVerification,
		"removeChatVerification": (*handler).removeChatVerification,
		"approveSuggestedPost":   (*handler).approveSuggestedPost,
		"declineSuggestedPost":   (*handler).declineSuggestedPost,
	})
	registerErrorMarkers("BOT_VERIFIER_FORBIDDEN", "SUGGESTED_POST_ALREADY_HANDLED", "SUGGESTED_POST_INVALID", "SUGGESTED_POST_BALANCE_TOO_LOW", "CUSTOM_DESCRIPTION_INVALID", "COMMENT_INVALID", "SEND_DATE_INVALID")
	registerMessageProjection(projectSuggestedPost)
}

func (h *handler) verifyUser(w http.ResponseWriter, r *http.Request, botID int64) {
	h.setVerification(w, r, botID, true, true)
}
func (h *handler) verifyChat(w http.ResponseWriter, r *http.Request, botID int64) {
	h.setVerification(w, r, botID, false, true)
}
func (h *handler) removeUserVerification(w http.ResponseWriter, r *http.Request, botID int64) {
	h.setVerification(w, r, botID, true, false)
}
func (h *handler) removeChatVerification(w http.ResponseWriter, r *http.Request, botID int64) {
	h.setVerification(w, r, botID, false, false)
}

func (h *handler) setVerification(w http.ResponseWriter, r *http.Request, botID int64, userOnly, enabled bool) {
	gateway, ok := h.gateway.(VerificationGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	var id int64
	if userOnly {
		id, err = strconv.ParseInt(values["user_id"], 10, 64)
		if err != nil || id <= 0 {
			writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
			return
		}
	} else {
		raw := strings.TrimSpace(values["chat_id"])
		if username, found := strings.CutPrefix(raw, "@"); found {
			if !validChatUsername(username) {
				err = errors.New("CHAT_ID_INVALID")
			} else {
				id, err = gateway.BotAPIResolveVerificationChatUsername(r.Context(), botID, username)
			}
		} else {
			id, err = strconv.ParseInt(raw, 10, 64)
		}
		if err != nil || id == 0 {
			writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
			return
		}
	}
	description := values["custom_description"]
	if !enabled {
		description = ""
	}
	if utf8.RuneCountInString(description) > 70 {
		writeAPIError(w, http.StatusBadRequest, "CUSTOM_DESCRIPTION_INVALID")
		return
	}
	if err := gateway.BotAPISetVerification(r.Context(), botID, id, enabled, description); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) approveSuggestedPost(w http.ResponseWriter, r *http.Request, botID int64) {
	h.toggleSuggestedPost(w, r, botID, false)
}
func (h *handler) declineSuggestedPost(w http.ResponseWriter, r *http.Request, botID int64) {
	h.toggleSuggestedPost(w, r, botID, true)
}

func (h *handler) toggleSuggestedPost(w http.ResponseWriter, r *http.Request, botID int64, reject bool) {
	gateway, ok := h.gateway.(SuggestedPostGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, err := strconv.ParseInt(values["chat_id"], 10, 64)
	if err != nil || chatID == 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	messageID, err := strconv.Atoi(values["message_id"])
	if err != nil || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	sendDate := 0
	if !reject && values["send_date"] != "" {
		sendDate, err = strconv.Atoi(values["send_date"])
		if err != nil || sendDate <= 0 {
			writeAPIError(w, http.StatusBadRequest, "SEND_DATE_INVALID")
			return
		}
	}
	comment := ""
	if reject {
		comment = values["comment"]
	}
	if utf8.RuneCountInString(comment) > 128 {
		writeAPIError(w, http.StatusBadRequest, "COMMENT_INVALID")
		return
	}
	if err := gateway.BotAPIToggleSuggestedPost(r.Context(), botID, chatID, messageID, reject, comment, sendDate); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func suggestedPostPrice(price *domain.SuggestedPostPrice) map[string]any {
	if price == nil {
		return nil
	}
	currency := "XTR"
	if price.Kind == domain.SuggestedPostPriceTON {
		currency = "TON"
	}
	return map[string]any{"currency": currency, "amount": price.Amount}
}

func projectSuggestedPost(msg domain.Message, out map[string]any) {
	if post := msg.SuggestedPost; post != nil {
		state := "pending"
		if post.Accepted {
			state = "approved"
		} else if post.Rejected {
			state = "declined"
		}
		info := map[string]any{"state": state}
		if price := suggestedPostPrice(post.Price); price != nil {
			info["price"] = price
		}
		if post.ScheduleDate > 0 {
			info["send_date"] = post.ScheduleDate
		}
		out["suggested_post_info"] = info
	}
	if msg.SavedPeer.Type == domain.PeerTypeUser && msg.SavedPeer.ID > 0 {
		out["direct_messages_topic"] = map[string]any{"topic_id": msg.SavedPeer.ID}
	}
	action := msg.Action
	if action == nil {
		return
	}
	service := map[string]any{}
	switch action.Type {
	case domain.ChannelActionSuggestedPostApproval:
		if action.SuggestedPostRejected {
			if action.SuggestedPostRejectComment != "" {
				service["comment"] = action.SuggestedPostRejectComment
			}
			out["suggested_post_declined"] = service
		} else if action.SuggestedPostBalanceTooLow {
			service["price"] = suggestedPostPrice(action.SuggestedPostPrice)
			out["suggested_post_approval_failed"] = service
		} else {
			service["send_date"] = action.SuggestedPostScheduleDate
			if price := suggestedPostPrice(action.SuggestedPostPrice); price != nil {
				service["price"] = price
			}
			out["suggested_post_approved"] = service
		}
	case domain.ChannelActionSuggestedPostSuccess:
		price := action.SuggestedPostPrice
		if price == nil {
			return
		}
		if price.Kind == domain.SuggestedPostPriceTON {
			service["currency"], service["amount"] = "TON", price.Amount
		} else {
			service["currency"] = "XTR"
			service["star_amount"] = map[string]any{"amount": price.Amount, "nanostar_amount": price.Nanos}
		}
		out["suggested_post_paid"] = service
	case domain.ChannelActionSuggestedPostRefund:
		reason := "post_deleted"
		if action.SuggestedPostPayerInitiated {
			reason = "payment_refunded"
		}
		service["reason"] = reason
		out["suggested_post_refunded"] = service
	}
}
