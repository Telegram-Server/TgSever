package rpc

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) BotAPIAnswerInlineQuery(ctx context.Context, botID, queryID int64, results domain.BotInlineResults) error {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return err
	}
	if queryID == 0 {
		return queryIDInvalidErr()
	}
	if !r.inlines.canAnswerContext(ctx, r.clock.Now(), botID, queryID) {
		return queryIDInvalidErr()
	}
	if len(results.Results) > domain.MaxBotInlineResults {
		return resultsTooMuchErr()
	}
	if len(results.NextOffset) > domain.MaxBotInlineNextOffsetLen {
		return nextOffsetInvalidErr()
	}
	if results.CacheTime < 0 {
		return tgerr400("CACHE_TIME_INVALID")
	}
	if results.SwitchPM != nil {
		if strings.TrimSpace(results.SwitchPM.Text) == "" || utf8.RuneCountInString(results.SwitchPM.Text) > domain.MaxBotInlineSwitchTextLen {
			return buttonInvalidErr()
		}
		if results.SwitchPM.StartParam == "" || !validInlineStartParam(results.SwitchPM.StartParam) {
			return startParamInvalidErr()
		}
	}
	if results.SwitchWeb != nil {
		if results.SwitchPM != nil || strings.TrimSpace(results.SwitchWeb.Text) == "" || utf8.RuneCountInString(results.SwitchWeb.Text) > domain.MaxBotInlineSwitchTextLen {
			return buttonInvalidErr()
		}
		if err := validateInlineWebURL(results.SwitchWeb.URL, true); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(results.Results))
	for i := range results.Results {
		item := &results.Results[i]
		if err := r.validateBotAPIInlineResult(ctx, botID, item); err != nil {
			return err
		}
		if seen[item.ID] {
			return resultIDDuplicateErr()
		}
		seen[item.ID] = true
	}
	if !r.inlines.resolveContext(ctx, r.clock.Now(), botID, queryID, results) {
		return queryIDInvalidErr()
	}
	return nil
}

func (r *Router) validateBotAPIInlineResult(ctx context.Context, botID int64, result *domain.BotInlineResult) error {
	if result.ID == "" || len(result.ID) > domain.MaxBotInlineResultIDLen {
		return resultIDInvalidErr()
	}
	if result.Type == "" {
		return resultTypeInvalidErr()
	}
	if result.RichMessageInput != nil {
		wire, err := tgInputRichMessageFromBotAPI(*result.RichMessageInput)
		if err != nil {
			return err
		}
		rich, err := r.domainRichMessageFromInput(ctx, wire)
		if err != nil {
			return err
		}
		if rich.IsZero() {
			return messageEmptyErr()
		}
		result.RichMessage = rich
		result.RichMessageInput = nil
	}
	if result.URL != "" {
		if err := validateInlineWebURL(result.URL, false); err != nil {
			return err
		}
	}
	if result.Thumb != nil {
		if err := validateInlineWebURL(result.Thumb.URL, true); err != nil {
			return err
		}
		if result.Thumb.AccessHash == 0 {
			result.Thumb.AccessHash = randomNonZeroInt64()
		}
	}
	if result.Content != nil {
		if err := validateInlineWebURL(result.Content.URL, true); err != nil {
			return err
		}
		if result.Content.AccessHash == 0 {
			result.Content.AccessHash = randomNonZeroInt64()
		}
	}
	if result.Media != nil {
		if result.Media.ProductInvoice != nil {
			if err := r.prepareInlineInvoice(ctx, botID, result.Media.ProductInvoice, result.InvoicePayload); err != nil {
				return err
			}
			result.InvoicePayload = ""
		}
		if result.Media.Geo != nil && result.Media.Geo.AccessHash == 0 {
			result.Media.Geo.AccessHash = randomNonZeroInt64()
		}
		if result.Media.Venue != nil && result.Media.Venue.Geo.AccessHash == 0 {
			result.Media.Venue.Geo.AccessHash = randomNonZeroInt64()
		}
		if result.Media.GeoLive != nil && result.Media.GeoLive.Geo.AccessHash == 0 {
			result.Media.GeoLive.Geo.AccessHash = randomNonZeroInt64()
		}
		if result.Media.Game != nil {
			media, err := r.resolveBotAPIInlineGame(ctx, botID, result.Media.Game.ShortName)
			if err != nil {
				return err
			}
			result.Media = media
			markup, err := gameReplyMarkup(*media.Game, result.ReplyMarkup)
			if err != nil {
				return err
			}
			result.ReplyMarkup = markup
		}
		if result.Media.Photo != nil {
			media, err := r.domainInlinePhotoMedia(ctx, &tg.InputPhoto{ID: result.Media.Photo.ID})
			if err != nil {
				return err
			}
			result.Media = media
		}
		if result.Media.Document != nil {
			media, err := r.domainInlineDocumentMedia(ctx, &tg.InputDocument{ID: result.Media.Document.ID})
			if err != nil {
				return err
			}
			result.Media = media
		}
		result.Media.InvertMedia = result.InvertMedia
	}
	if result.Message == "" && result.RichMessage.IsZero() && (result.Media == nil || result.Media.IsZero()) && !result.MediaAuto {
		return messageEmptyErr()
	}
	if utf8.RuneCountInString(result.Message) > domain.MaxMessageTextLength {
		return messageTooLongErr()
	}
	if err := domain.ValidateReplyMarkup(result.ReplyMarkup); err != nil {
		return replyMarkupErr(err)
	}
	if result.ReplyMarkup != nil && result.ReplyMarkup.Kind() != domain.MessageReplyMarkupInline {
		return buttonInvalidErr()
	}
	return r.prepareTelegramLoginMarkup(ctx, botID, result.ReplyMarkup)
}

func (r *Router) BotAPISavePreparedKeyboardButton(ctx context.Context, botID, userID int64, button domain.BotRequestedWebViewButton) (string, error) {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return "", err
	}
	if r.deps.Bots == nil || r.deps.Users == nil {
		return "", internalErr()
	}
	target, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return "", internalErr()
	}
	if !found || target.Bot {
		return "", userIDInvalidErr()
	}
	if err := r.ensureBotAccess(ctx, botID, userID); err != nil {
		return "", err
	}
	button.BotUserID, button.UserID = botID, userID
	saved, err := r.deps.Bots.SaveRequestedWebViewButton(ctx, button)
	if err != nil {
		return "", err
	}
	return saved.WebAppReqID, nil
}

func (r *Router) enqueueInlineQueryObject(ctx context.Context, botID, userID, queryID int64, query, offset, peerType string, geo *domain.MessageGeoPoint) {
	payload := map[string]any{"id": strconv.FormatInt(queryID, 10), "from": r.inlineUserObject(ctx, userID), "query": query, "offset": offset}
	switch peerType {
	case "same_bot_pm", "pm", "bot_pm":
		payload["chat_type"] = "private"
	case "chat":
		payload["chat_type"] = "group"
	case "megagroup":
		payload["chat_type"] = "supergroup"
	case "broadcast":
		payload["chat_type"] = "channel"
	}
	if geo != nil {
		payload["location"] = inlineLocationObject(geo)
	}
	r.enqueueBotAPIObject(ctx, botID, domain.BotAPIUpdateKind("inline_query"), payload)
}

func (r *Router) inlineUserObject(ctx context.Context, userID int64) map[string]any {
	user := domain.User{ID: userID}
	if r.deps.Users != nil {
		if found, ok, err := r.deps.Users.ByID(ctx, userID, userID); err == nil && ok {
			user = found
		}
	}
	first := user.FirstName
	if strings.TrimSpace(first) == "" {
		first = "User " + strconv.FormatInt(userID, 10)
	}
	out := map[string]any{"id": userID, "is_bot": user.Bot, "first_name": first}
	if user.LastName != "" {
		out["last_name"] = user.LastName
	}
	if user.Username != "" {
		out["username"] = user.Username
	}
	return out
}

func inlineLocationObject(geo *domain.MessageGeoPoint) map[string]any {
	out := map[string]any{"latitude": geo.Lat, "longitude": geo.Long}
	if geo.AccuracyRadius > 0 {
		out["horizontal_accuracy"] = geo.AccuracyRadius
	}
	return out
}

func inlineDomainMessageID(id tg.InputBotInlineMessageIDClass) (domain.BotInlineMessageID, bool) {
	v, ok := id.(*tg.InputBotInlineMessageID64)
	if !ok {
		return domain.BotInlineMessageID{}, false
	}
	return domain.BotInlineMessageID{DCID: v.DCID, OwnerID: v.OwnerID, ID: v.ID, AccessHash: v.AccessHash}, true
}
