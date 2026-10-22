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

type ProfilesExtendedGateway interface {
	BotAPIGetUserProfilePhotos(context.Context, int64, int64, int, int) ([]domain.Photo, int, error)
	BotAPIGetUserProfileAudios(context.Context, int64, int64, int, int) (domain.SavedMusicList, error)
	BotAPIGetUserPersonalChatMessages(context.Context, int64, int64, int) ([]domain.Message, []domain.User, []domain.Channel, error)
	BotAPIGetUserChatBoosts(context.Context, int64, int64, int64) ([]domain.PremiumBoostSlot, []domain.User, error)
	BotAPISetMyDefaultAdministratorRights(context.Context, int64, bool, domain.ChannelAdminRights) error
	BotAPIGetMyDefaultAdministratorRights(context.Context, int64, bool) (domain.ChannelAdminRights, error)
	BotAPISetMyProfilePhoto(context.Context, int64, []byte, bool, float64) error
	BotAPIRemoveMyProfilePhoto(context.Context, int64) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"getUserProfilePhotos":            (*handler).getUserProfilePhotos,
		"getUserProfileAudios":            (*handler).getUserProfileAudios,
		"getUserPersonalChatMessages":     (*handler).getUserPersonalChatMessages,
		"getUserChatBoosts":               (*handler).getUserChatBoosts,
		"setMyDefaultAdministratorRights": (*handler).setMyDefaultAdministratorRights,
		"getMyDefaultAdministratorRights": (*handler).getMyDefaultAdministratorRights,
		"setMyProfilePhoto":               (*handler).setMyProfilePhoto,
		"removeMyProfilePhoto":            (*handler).removeMyProfilePhoto,
	})
	registerErrorMarkers("LIMIT_INVALID", "BOT_MANAGER_INVALID", "BOT_ACCESS_DENIED", "BOT_INSTANCE_CLOSED", "PHOTO_INVALID")
}
func (h *handler) profilesRequest(w http.ResponseWriter, r *http.Request) (ProfilesExtendedGateway, map[string]string, bool) {
	g, ok := h.gateway.(ProfilesExtendedGateway)
	if !ok {
		writeAPIError(w, 501, "METHOD_NOT_FOUND")
		return nil, nil, false
	}
	v, err := requestValues(r)
	if err != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return nil, nil, false
	}
	return g, v, true
}
func profileUserID(v map[string]string) (int64, error) {
	id, err := strconv.ParseInt(v["user_id"], 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("USER_ID_INVALID")
	}
	return id, nil
}
func profilePage(v map[string]string, maxLimit, defaultLimit int) (int, int, error) {
	offset, limit := 0, defaultLimit
	var err error
	if raw := v["offset"]; raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, errors.New("LIMIT_INVALID")
		}
	}
	if raw := v["limit"]; raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, errors.New("LIMIT_INVALID")
		}
	}
	if offset < 0 || limit < 1 || limit > maxLimit {
		return 0, 0, errors.New("LIMIT_INVALID")
	}
	return offset, limit, nil
}
func profileError(w http.ResponseWriter, err error) { writeAPIError(w, 400, apiErrorDescription(err)) }
func (h *handler) getUserProfilePhotos(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	id, err := profileUserID(v)
	if err != nil {
		profileError(w, err)
		return
	}
	offset, limit, err := profilePage(v, 100, 100)
	if err != nil {
		profileError(w, err)
		return
	}
	photos, total, err := g.BotAPIGetUserProfilePhotos(r.Context(), botID, id, offset, limit)
	if err != nil {
		profileError(w, err)
		return
	}
	out := make([][]map[string]any, 0, len(photos))
	for _, p := range photos {
		out = append(out, apiPhotoSizes(p))
	}
	writeAPIOK(w, map[string]any{"total_count": total, "photos": out})
}
func (h *handler) getUserProfileAudios(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	id, err := profileUserID(v)
	if err != nil {
		profileError(w, err)
		return
	}
	offset, limit, err := profilePage(v, 100, 100)
	if err != nil {
		profileError(w, err)
		return
	}
	list, err := g.BotAPIGetUserProfileAudios(r.Context(), botID, id, offset, limit)
	if err != nil {
		profileError(w, err)
		return
	}
	out := make([]any, 0, len(list.Documents))
	for _, d := range list.Documents {
		if audio, ok := apiDocumentMedia(d)["audio"]; ok {
			out = append(out, audio)
		}
	}
	writeAPIOK(w, map[string]any{"total_count": list.Count, "audios": out})
}
func (h *handler) getUserPersonalChatMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	id, err := profileUserID(v)
	if err != nil {
		profileError(w, err)
		return
	}
	_, limit, err := profilePage(v, 20, 0)
	if err != nil {
		profileError(w, err)
		return
	}
	messages, users, chats, err := g.BotAPIGetUserPersonalChatMessages(r.Context(), botID, id, limit)
	if err != nil {
		profileError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		out = append(out, apiMessage(m, users, chats))
	}
	writeAPIOK(w, out)
}
func (h *handler) getUserChatBoosts(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	id, err := profileUserID(v)
	if err != nil {
		profileError(w, err)
		return
	}
	chatID, err := h.chatIDParam(r.Context(), botID, v["chat_id"])
	if err != nil {
		profileError(w, err)
		return
	}
	slots, users, err := g.BotAPIGetUserChatBoosts(r.Context(), botID, chatID, id)
	if err != nil {
		profileError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(slots))
	byID := map[int64]domain.User{}
	for _, u := range users {
		byID[u.ID] = u
	}
	for _, s := range slots {
		source := map[string]any{"source": "premium", "user": apiUser(byID[s.UserID])}
		if s.Gift {
			source["source"] = "gift_code"
		}
		if s.Giveaway {
			source["source"] = "giveaway"
			source["giveaway_message_id"] = s.GiveawayMsgID
			if s.Stars > 0 {
				source["prize_star_count"] = s.Stars
				delete(source, "user")
			}
			if s.Unclaimed {
				delete(source, "user")
				source["is_unclaimed"] = true
			}
		}
		out = append(out, map[string]any{"boost_id": strconv.FormatInt(s.UserID, 10) + ":" + strconv.Itoa(s.Slot) + ":" + strconv.Itoa(s.Date), "add_date": s.Date, "expiration_date": s.Expires, "source": source})
	}
	writeAPIOK(w, map[string]any{"boosts": out})
}
func (h *handler) setMyDefaultAdministratorRights(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	var rights domain.ChannelAdminRights
	if raw := strings.TrimSpace(v["rights"]); raw != "" {
		var fields map[string]bool
		if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
			writeAPIError(w, 400, "RIGHTS_INVALID")
			return
		}
		for _, f := range adminRightFields {
			*f.get(&rights) = fields[f.name]
		}
	}
	channels, err := profileBool(v["for_channels"])
	if err != nil {
		profileError(w, err)
		return
	}
	if err = g.BotAPISetMyDefaultAdministratorRights(r.Context(), botID, channels, rights); err != nil {
		profileError(w, err)
		return
	}
	writeAPIOK(w, true)
}
func profileBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New("BAD_REQUEST")
	}
	return b, nil
}
func (h *handler) getMyDefaultAdministratorRights(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	channels, err := profileBool(v["for_channels"])
	if err != nil {
		profileError(w, err)
		return
	}
	rights, err := g.BotAPIGetMyDefaultAdministratorRights(r.Context(), botID, channels)
	if err != nil {
		profileError(w, err)
		return
	}
	writeAPIOK(w, apiAdministratorRights(rights))
}
func (h *handler) setMyProfilePhoto(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gateway.(ProfilesExtendedGateway)
	if !ok {
		writeAPIError(w, 501, "METHOD_NOT_FOUND")
		return
	}
	v, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return
	}
	var input struct {
		Type      string  `json:"type"`
		Photo     string  `json:"photo"`
		Animation string  `json:"animation"`
		Timestamp float64 `json:"main_frame_timestamp"`
	}
	if json.Unmarshal([]byte(v["photo"]), &input) != nil {
		writeAPIError(w, 400, "PHOTO_INVALID")
		return
	}
	raw := input.Photo
	animated := false
	if input.Type == "animated" {
		raw = input.Animation
		animated = true
	} else if input.Type != "static" {
		writeAPIError(w, 400, "PHOTO_INVALID")
		return
	}
	field, ok := strings.CutPrefix(raw, "attach://")
	if !ok || field == "" {
		writeAPIError(w, 400, "PHOTO_INVALID")
		return
	}
	file, ok := files[field]
	if !ok || len(file.Bytes) == 0 {
		writeAPIError(w, 400, "PHOTO_INVALID")
		return
	}
	if err = g.BotAPISetMyProfilePhoto(r.Context(), botID, file.Bytes, animated, input.Timestamp); err != nil {
		profileError(w, err)
		return
	}
	writeAPIOK(w, true)
}
func (h *handler) removeMyProfilePhoto(w http.ResponseWriter, r *http.Request, botID int64) {
	g, _, ok := h.profilesRequest(w, r)
	if !ok {
		return
	}
	if err := g.BotAPIRemoveMyProfilePhoto(r.Context(), botID); err != nil {
		profileError(w, err)
		return
	}
	writeAPIOK(w, true)
}
