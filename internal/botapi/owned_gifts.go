package botapi

import (
	"context"
	"net/http"
	"strconv"

	"telesrv/internal/domain"
)

type OwnedGiftsGateway interface {
	BotAPIGetUserGifts(context.Context, int64, int64, domain.SavedStarGiftFilter) (domain.BotAPIOwnedGiftPage, error)
	BotAPIGetChatGifts(context.Context, int64, int64, domain.SavedStarGiftFilter) (domain.BotAPIOwnedGiftPage, error)
	BotAPIConvertGiftToStars(context.Context, int64, string, string) (bool, error)
	BotAPIUpgradeGift(context.Context, int64, string, string, bool, int64) (bool, error)
	BotAPITransferGift(context.Context, int64, string, string, int64, int64) (bool, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"getUserGifts":       (*handler).getUserGifts,
		"getChatGifts":       (*handler).getChatGifts,
		"convertGiftToStars": (*handler).convertGiftToStars,
		"upgradeGift":        (*handler).upgradeGift,
		"transferGift":       (*handler).transferGift,
	})
	registerErrorMarkers("GIFT_INVALID", "GIFT_UPGRADE_UNAVAILABLE", "GIFT_TRANSFER_UNAVAILABLE", "STAR_COUNT_INVALID", "OFFSET_INVALID", "LIMIT_INVALID", "RECIPIENT_NOT_ACTIVE")
}

// ParseOwnedGiftFilter is shared with getBusinessAccountGifts. Filtering belongs
// in the store before pagination, never in the JSON projection.
func ParseOwnedGiftFilter(values map[string]string) (domain.SavedStarGiftFilter, bool) {
	filter := domain.SavedStarGiftFilter{BotAPI: true, Offset: values["offset"], Limit: 100}
	if raw := values["limit"]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return filter, false
		}
		filter.Limit = n
	}
	fields := [...]struct {
		name   string
		target *bool
	}{
		{"exclude_unsaved", &filter.ExcludeUnsaved}, {"exclude_saved", &filter.ExcludeSaved},
		{"exclude_unlimited", &filter.ExcludeUnlimited}, {"exclude_unique", &filter.ExcludeUnique},
		{"exclude_limited_upgradable", &filter.ExcludeLimitedUpgradable},
		{"exclude_limited_non_upgradable", &filter.ExcludeLimitedNonUpgradable},
		{"exclude_from_blockchain", &filter.ExcludeFromBlockchain}, {"sort_by_price", &filter.SortByValue},
	}
	for _, field := range fields {
		if raw := values[field.name]; raw != "" {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return filter, false
			}
			*field.target = value
		}
	}
	return filter, true
}

func (h *handler) ownedGiftRequest(w http.ResponseWriter, r *http.Request) (OwnedGiftsGateway, map[string]string, bool) {
	gateway, ok := h.gateway.(OwnedGiftsGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, false
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return nil, nil, false
	}
	return gateway, values, true
}

func (h *handler) getUserGifts(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.ownedGiftRequest(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(values["user_id"], 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
		return
	}
	filter, ok := ParseOwnedGiftFilter(values)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	page, err := gateway.BotAPIGetUserGifts(r.Context(), botID, userID, filter)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, APIOwnedGifts(page))
}

func (h *handler) getChatGifts(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.ownedGiftRequest(w, r)
	if !ok {
		return
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	filter, ok := ParseOwnedGiftFilter(values)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	page, err := gateway.BotAPIGetChatGifts(r.Context(), botID, chatID, filter)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, APIOwnedGifts(page))
}

func (h *handler) convertGiftToStars(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.ownedGiftRequest(w, r)
	if !ok {
		return
	}
	result, err := gateway.BotAPIConvertGiftToStars(r.Context(), botID, values["business_connection_id"], values["owned_gift_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func (h *handler) upgradeGift(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.ownedGiftRequest(w, r)
	if !ok {
		return
	}
	keep, stars := false, int64(0)
	var err error
	if values["keep_original_details"] != "" {
		keep, err = strconv.ParseBool(values["keep_original_details"])
	}
	if err == nil && values["star_count"] != "" {
		stars, err = strconv.ParseInt(values["star_count"], 10, 64)
	}
	if err != nil || stars < 0 {
		writeAPIError(w, http.StatusBadRequest, "STAR_COUNT_INVALID")
		return
	}
	result, err := gateway.BotAPIUpgradeGift(r.Context(), botID, values["business_connection_id"], values["owned_gift_id"], keep, stars)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func (h *handler) transferGift(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.ownedGiftRequest(w, r)
	if !ok {
		return
	}
	to, err := strconv.ParseInt(values["new_owner_chat_id"], 10, 64)
	if err != nil || to == 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	stars := int64(0)
	if values["star_count"] != "" {
		stars, err = strconv.ParseInt(values["star_count"], 10, 64)
	}
	if err != nil || stars < 0 {
		writeAPIError(w, http.StatusBadRequest, "STAR_COUNT_INVALID")
		return
	}
	result, err := gateway.BotAPITransferGift(r.Context(), botID, values["business_connection_id"], values["owned_gift_id"], to, stars)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func APIOwnedGifts(page domain.BotAPIOwnedGiftPage) map[string]any {
	gifts := make([]map[string]any, 0, len(page.Gifts))
	for _, item := range page.Gifts {
		gifts = append(gifts, apiOwnedGift(item))
	}
	out := map[string]any{"total_count": page.Count, "gifts": gifts}
	if page.NextOffset != "" {
		out["next_offset"] = page.NextOffset
	}
	return out
}

func apiOwnedGift(item domain.BotAPIOwnedGift) map[string]any {
	saved := item.Saved
	out := map[string]any{"send_date": saved.Date}
	if item.Sender != nil {
		out["sender_user"] = apiUser(*item.Sender)
	}
	if item.Business {
		if saved.MsgID > 0 {
			out["owned_gift_id"] = strconv.Itoa(saved.MsgID)
		}
		if !saved.Unsaved {
			out["is_saved"] = true
		}
	}
	if saved.Unique != nil {
		out["type"] = "unique"
		out["gift"] = apiOwnedUniqueGift(*saved.Unique, item.Publisher)
		if saved.CanTransferAt > 0 {
			out["next_transfer_date"] = saved.CanTransferAt
		}
		if item.Business && item.CanTransfer && saved.LifecycleStatus.Live() && saved.Unique.OwnerAddress == "" && !saved.Unique.Burned && saved.Unique.ResellAmount == nil {
			out["transfer_star_count"] = saved.TransferStars
			if saved.CanTransferAt <= item.Now {
				out["can_be_transferred"] = true
			}
		}
		return out
	}
	out["type"], out["gift"] = "regular", apiStarGift(item.Gift)
	if item.Publisher != nil {
		out["gift"].(map[string]any)["publisher_chat"] = apiGiftPublisher(*item.Publisher)
	}
	if saved.NameHidden {
		out["is_private"] = true
	}
	if saved.Message != "" {
		out["text"] = saved.Message
	}
	if entities := apiMessageEntities(saved.MessageEntities, item.EntityUsers); len(entities) > 0 {
		out["entities"] = entities
	}
	if saved.PrepaidUpgradeStars > 0 {
		out["prepaid_upgrade_star_count"] = saved.PrepaidUpgradeStars
	}
	if saved.GiftNum > 0 {
		out["unique_gift_number"] = saved.GiftNum
	}
	if item.Business {
		if item.Gift.UpgradeStars > 0 && item.Gift.UpgradeIssued < item.Gift.UpgradeTotal {
			out["can_be_upgraded"] = true
		}
		if saved.ConvertStars > 0 {
			out["convert_star_count"] = saved.ConvertStars
		}
		if saved.PrepaidUpgradeHash != "" && saved.PrepaidUpgradeStars > 0 {
			out["is_upgrade_separate"] = true
		}
	}
	return out
}

func apiGiftPublisher(c domain.Channel) map[string]any {
	kind := "supergroup"
	if c.Broadcast && !c.Megagroup {
		kind = "channel"
	}
	out := map[string]any{"id": botAPIChannelChatIDMin - c.ID, "type": kind, "title": c.Title}
	if c.Username != "" {
		out["username"] = c.Username
	}
	return out
}

func apiOwnedUniqueGift(g domain.UniqueStarGift, publisher *domain.Channel) map[string]any {
	attribute := func(a domain.StarGiftCollectibleAttribute) map[string]any {
		out := map[string]any{"name": a.Name, "rarity_per_mille": a.RarityPermille}
		if a.Document != nil {
			out["sticker"] = apiSticker(*a.Document)
		}
		return out
	}
	model := attribute(g.Model)
	if g.Model.Crafted {
		model["rarity_per_mille"] = 0
		if g.Model.RarityKind != domain.StarGiftRarityPermille {
			model["rarity"] = string(g.Model.RarityKind)
		}
	}
	backdrop := map[string]any{"name": g.Backdrop.Name, "rarity_per_mille": g.Backdrop.RarityPermille,
		"colors": map[string]any{"center_color": g.Backdrop.CenterColor, "edge_color": g.Backdrop.EdgeColor, "symbol_color": g.Backdrop.PatternColor, "text_color": g.Backdrop.TextColor}}
	out := map[string]any{"gift_id": strconv.FormatInt(g.GiftID, 10), "base_name": g.Title, "name": g.Slug, "number": g.Num, "model": model, "symbol": attribute(g.Pattern), "backdrop": backdrop}
	if g.RequirePremium {
		out["is_premium"] = true
	}
	if g.Burned {
		out["is_burned"] = true
	}
	if g.OwnerAddress != "" {
		out["is_from_blockchain"] = true
	}
	if publisher != nil {
		out["publisher_chat"] = apiGiftPublisher(*publisher)
	}
	return out
}
