package botapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

type BusinessGateway interface {
	BotAPIGetBusinessConnection(context.Context, int64, string) (domain.ConnectedBusinessBot, domain.User, error)
	BotAPIReadBusinessMessage(context.Context, int64, string, int64, int) (bool, error)
	BotAPIDeleteBusinessMessages(context.Context, int64, string, []int) (bool, error)
	BotAPISetBusinessAccountName(context.Context, int64, string, string, string) (bool, error)
	BotAPISetBusinessAccountUsername(context.Context, int64, string, string) (bool, error)
	BotAPISetBusinessAccountBio(context.Context, int64, string, string) (bool, error)
	BotAPISetBusinessAccountProfilePhoto(context.Context, int64, string, []byte, bool, float64, bool) (bool, error)
	BotAPIRemoveBusinessAccountProfilePhoto(context.Context, int64, string, bool) (bool, error)
	BotAPISetBusinessAccountGiftSettings(context.Context, int64, string, bool, domain.DisallowedGifts) (bool, error)
	BotAPIGetBusinessAccountStarBalance(context.Context, int64, string) (domain.StarsBalance, error)
	BotAPITransferBusinessAccountStars(context.Context, int64, string, int64) (bool, error)
	BotAPIGetBusinessAccountGifts(context.Context, int64, string, domain.SavedStarGiftFilter) (domain.BotAPIOwnedGiftPage, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"getBusinessConnection":             (*handler).getBusinessConnection,
		"readBusinessMessage":               (*handler).readBusinessMessage,
		"deleteBusinessMessages":            (*handler).deleteBusinessMessages,
		"setBusinessAccountName":            (*handler).setBusinessAccountName,
		"setBusinessAccountUsername":        (*handler).setBusinessAccountUsername,
		"setBusinessAccountBio":             (*handler).setBusinessAccountBio,
		"setBusinessAccountProfilePhoto":    (*handler).setBusinessAccountProfilePhoto,
		"removeBusinessAccountProfilePhoto": (*handler).removeBusinessAccountProfilePhoto,
		"setBusinessAccountGiftSettings":    (*handler).setBusinessAccountGiftSettings,
		"getBusinessAccountStarBalance":     (*handler).getBusinessAccountStarBalance,
		"transferBusinessAccountStars":      (*handler).transferBusinessAccountStars,
		"getBusinessAccountGifts":           (*handler).getBusinessAccountGifts,
	})
	registerErrorMarkers("BUSINESS_CONNECTION_INVALID", "BUSINESS_CONNECTION_DISABLED", "BUSINESS_BOT_RIGHT_REQUIRED", "BUSINESS_PEER_DISABLED", "BUSINESS_PEER_INVALID", "BUSINESS_CHAT_NOT_ACTIVE", "MESSAGE_DELETE_FORBIDDEN")
}

// BusinessConnectionObject is also used for the immutable connection event.
func BusinessConnectionObject(connection domain.ConnectedBusinessBot, user domain.User) map[string]any {
	rights := map[string]any{}
	for _, right := range []string{"can_reply", "can_read_messages", "can_delete_sent_messages", "can_delete_all_messages", "can_edit_name", "can_edit_bio", "can_edit_profile_photo", "can_edit_username", "can_change_gift_settings", "can_view_gifts_and_stars", "can_convert_gifts_to_stars", "can_transfer_and_upgrade_gifts", "can_transfer_stars", "can_manage_stories"} {
		if connection.Rights.Allows(right) {
			rights[right] = true
		}
	}
	return map[string]any{"id": connection.ConnectionID, "user": apiUser(user), "user_chat_id": user.ID, "date": connection.CreatedAtUnix, "rights": rights, "is_enabled": !connection.Disabled}
}

func (h *handler) businessRequest(w http.ResponseWriter, r *http.Request) (BusinessGateway, map[string]string, bool) {
	gateway, ok := h.gateway.(BusinessGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, false
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return nil, nil, false
	}
	if strings.TrimSpace(values["business_connection_id"]) == "" {
		writeAPIError(w, http.StatusBadRequest, "BUSINESS_CONNECTION_INVALID")
		return nil, nil, false
	}
	return gateway, values, true
}

func businessResult(w http.ResponseWriter, result bool, err error) {
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func (h *handler) getBusinessConnection(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	connection, user, err := gateway.BotAPIGetBusinessConnection(r.Context(), botID, values["business_connection_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, BusinessConnectionObject(connection, user))
}

func (h *handler) readBusinessMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	chatID, err := strconv.ParseInt(values["chat_id"], 10, 64)
	if err != nil || chatID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	messageID, err := strconv.Atoi(values["message_id"])
	if err != nil || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	result, err := gateway.BotAPIReadBusinessMessage(r.Context(), botID, values["business_connection_id"], chatID, messageID)
	businessResult(w, result, err)
}

func (h *handler) deleteBusinessMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	var ids []int
	if err := json.Unmarshal([]byte(values["message_ids"]), &ids); err != nil || len(ids) < 1 || len(ids) > 100 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
			return
		}
	}
	result, err := gateway.BotAPIDeleteBusinessMessages(r.Context(), botID, values["business_connection_id"], ids)
	businessResult(w, result, err)
}

func (h *handler) setBusinessAccountName(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	result, err := gateway.BotAPISetBusinessAccountName(r.Context(), botID, values["business_connection_id"], values["first_name"], values["last_name"])
	businessResult(w, result, err)
}

func (h *handler) setBusinessAccountUsername(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	result, err := gateway.BotAPISetBusinessAccountUsername(r.Context(), botID, values["business_connection_id"], values["username"])
	businessResult(w, result, err)
}

func (h *handler) setBusinessAccountBio(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	result, err := gateway.BotAPISetBusinessAccountBio(r.Context(), botID, values["business_connection_id"], values["bio"])
	businessResult(w, result, err)
}

func (h *handler) setBusinessAccountProfilePhoto(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BusinessGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var photo struct {
		Type      string  `json:"type"`
		Photo     string  `json:"photo"`
		Animation string  `json:"animation"`
		Timestamp float64 `json:"main_frame_timestamp"`
	}
	if err := json.Unmarshal([]byte(values["photo"]), &photo); err != nil {
		writeAPIError(w, http.StatusBadRequest, "PHOTO_INVALID")
		return
	}
	animated := photo.Type == "animated"
	field := photo.Photo
	if animated {
		field = photo.Animation
	}
	if photo.Type != "static" && !animated || !strings.HasPrefix(field, "attach://") || math.IsNaN(photo.Timestamp) || math.IsInf(photo.Timestamp, 0) || photo.Timestamp < 0 {
		writeAPIError(w, http.StatusBadRequest, "PHOTO_INVALID")
		return
	}
	file, found := files[strings.TrimPrefix(field, "attach://")]
	if !found || len(file.Bytes) == 0 {
		writeAPIError(w, http.StatusBadRequest, "PHOTO_INVALID")
		return
	}
	public := false
	if values["is_public"] != "" {
		public, err = strconv.ParseBool(values["is_public"])
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	result, err := gateway.BotAPISetBusinessAccountProfilePhoto(r.Context(), botID, values["business_connection_id"], file.Bytes, animated, photo.Timestamp, public)
	businessResult(w, result, err)
}

func (h *handler) removeBusinessAccountProfilePhoto(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	public := false
	var err error
	if values["is_public"] != "" {
		public, err = strconv.ParseBool(values["is_public"])
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	result, err := gateway.BotAPIRemoveBusinessAccountProfilePhoto(r.Context(), botID, values["business_connection_id"], public)
	businessResult(w, result, err)
}

func (h *handler) setBusinessAccountGiftSettings(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	show, err := strconv.ParseBool(values["show_gift_button"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	var accepted struct {
		Unlimited *bool `json:"unlimited_gifts"`
		Limited   *bool `json:"limited_gifts"`
		Unique    *bool `json:"unique_gifts"`
		Premium   *bool `json:"premium_subscription"`
		Channels  *bool `json:"gifts_from_channels"`
	}
	if err := json.Unmarshal([]byte(values["accepted_gift_types"]), &accepted); err != nil || accepted.Unlimited == nil || accepted.Limited == nil || accepted.Unique == nil || accepted.Premium == nil || accepted.Channels == nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	denied := domain.DisallowedGifts{UnlimitedStargifts: !*accepted.Unlimited, LimitedStargifts: !*accepted.Limited, UniqueStargifts: !*accepted.Unique, PremiumGifts: !*accepted.Premium, StargiftsFromChannel: !*accepted.Channels}
	result, err := gateway.BotAPISetBusinessAccountGiftSettings(r.Context(), botID, values["business_connection_id"], show, denied)
	businessResult(w, result, err)
}

func (h *handler) getBusinessAccountStarBalance(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	balance, err := gateway.BotAPIGetBusinessAccountStarBalance(r.Context(), botID, values["business_connection_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, map[string]any{"amount": balance.Balance})
}

func (h *handler) transferBusinessAccountStars(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	count, err := strconv.ParseInt(values["star_count"], 10, 64)
	if err != nil || count < 1 || count > 10000 {
		writeAPIError(w, http.StatusBadRequest, "STAR_COUNT_INVALID")
		return
	}
	result, err := gateway.BotAPITransferBusinessAccountStars(r.Context(), botID, values["business_connection_id"], count)
	businessResult(w, result, err)
}

func (h *handler) getBusinessAccountGifts(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.businessRequest(w, r)
	if !ok {
		return
	}
	filter, ok := ParseOwnedGiftFilter(values)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	page, err := gateway.BotAPIGetBusinessAccountGifts(r.Context(), botID, values["business_connection_id"], filter)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, APIOwnedGifts(page))
}
