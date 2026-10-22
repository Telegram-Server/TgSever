package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

type ephemeralSendTarget struct {
	receiverUserID              int64
	callbackQueryID             int64
	replyToEphemeralID          int
	replaceCallbackQueryMessage bool
	topMessageID                int
}

func parseEphemeralSendTarget(values map[string]string) (ephemeralSendTarget, bool, error) {
	var result ephemeralSendTarget
	for _, legacy := range []string{"receiver_user_id", "callback_query_id", "replace_callback_query_message"} {
		if _, present := values[legacy]; present {
			return result, false, errors.New("EPHEMERAL_MESSAGE_PARAMETERS_INVALID")
		}
	}
	rawParameters, present := values["ephemeral_message_parameters"]
	if !present {
		var reply map[string]json.RawMessage
		if raw := values["reply_parameters"]; raw != "" {
			if json.Unmarshal([]byte(raw), &reply) != nil {
				return result, false, errors.New("REPLY_PARAMETERS_INVALID")
			}
			if _, exists := reply["ephemeral_message_id"]; exists {
				return result, false, errors.New("EPHEMERAL_MESSAGE_PARAMETERS_INVALID")
			}
		}
		return result, false, nil
	}
	var parameters struct {
		ReceiverUserID              int64  `json:"receiver_user_id"`
		CallbackQueryID             string `json:"callback_query_id"`
		ReplaceCallbackQueryMessage bool   `json:"replace_callback_query_message"`
	}
	decoder := json.NewDecoder(strings.NewReader(rawParameters))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&parameters) != nil || decoder.Decode(new(any)) != io.EOF || parameters.ReceiverUserID <= 0 {
		return result, false, errors.New("EPHEMERAL_MESSAGE_PARAMETERS_INVALID")
	}
	var parameterFields map[string]json.RawMessage
	if json.Unmarshal([]byte(rawParameters), &parameterFields) != nil {
		return result, false, errors.New("EPHEMERAL_MESSAGE_PARAMETERS_INVALID")
	}
	for _, value := range parameterFields {
		if strings.TrimSpace(string(value)) == "null" {
			return result, false, errors.New("EPHEMERAL_MESSAGE_PARAMETERS_INVALID")
		}
	}
	if strings.TrimSpace(values["business_connection_id"]) != "" {
		return result, false, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	if _, present := values["direct_messages_topic_id"]; present {
		return result, false, errors.New("DIRECT_MESSAGES_TOPIC_ID_INVALID")
	}
	if _, present := values["suggested_post_parameters"]; present {
		return result, false, errors.New("SUGGESTED_POST_PARAMETERS_INVALID")
	}
	result.receiverUserID = parameters.ReceiverUserID
	result.replaceCallbackQueryMessage = parameters.ReplaceCallbackQueryMessage
	callbackRaw := parameters.CallbackQueryID
	reply, err := parseBotAPIReplyParameters(values)
	if err != nil {
		return result, false, err
	}
	if reply != nil {
		if reply.MessageID != 0 || reply.ChatID != "" || reply.AllowSendingWithoutReply || reply.ChecklistTaskID != 0 || reply.PollOptionID != "" {
			return result, false, errors.New("REPLY_PARAMETERS_INVALID")
		}
		result.replyToEphemeralID = reply.EphemeralMessageID
	}
	if callbackRaw != "" {
		result.callbackQueryID, err = strconv.ParseInt(callbackRaw, 10, 64)
		if err != nil || result.callbackQueryID == 0 {
			return result, false, errors.New("QUERY_ID_INVALID")
		}
	}
	if result.replaceCallbackQueryMessage && result.callbackQueryID == 0 {
		return result, false, errors.New("QUERY_ID_INVALID")
	}
	if (result.callbackQueryID != 0 && result.replyToEphemeralID != 0) || strings.TrimSpace(values["reply_to_message_id"]) != "" {
		return result, false, errors.New("REPLY_PARAMETERS_INVALID")
	}
	if raw, present := values["message_thread_id"]; present {
		result.topMessageID, err = strconv.Atoi(raw)
		if err != nil || result.topMessageID <= 0 || result.topMessageID > domain.MaxMessageBoxID {
			return result, false, errors.New("MESSAGE_THREAD_ID_INVALID")
		}
	}
	return result, true, nil
}

func botAPIMessageOptions(values map[string]string) (domain.BotAPIMessageOptions, error) {
	options := domain.BotAPIMessageOptions{BusinessConnectionID: strings.TrimSpace(values["business_connection_id"])}
	if raw, present := values["direct_messages_topic_id"]; present {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 || id > domain.MaxMessageBoxID {
			return options, errors.New("DIRECT_MESSAGES_TOPIC_ID_INVALID")
		}
		options.DirectMessagesTopicID = id
	}
	if raw, present := values["suggested_post_parameters"]; present {
		post, err := parseBotAPISuggestedPost(raw)
		if err != nil {
			return options, err
		}
		options.SuggestedPost = post
	}
	if raw, present := values["message_thread_id"]; present {
		threadID, err := strconv.Atoi(raw)
		if err != nil || threadID <= 0 || threadID > domain.MaxMessageBoxID {
			return options, errors.New("MESSAGE_THREAD_ID_INVALID")
		}
		options.MessageThreadID = threadID
	}
	if raw, present := values["allow_paid_broadcast"]; present {
		if raw != "true" && raw != "false" {
			return options, errors.New("ALLOW_PAID_BROADCAST_INVALID")
		}
		options.AllowPaidBroadcast = raw == "true"
	}
	if raw, present := values["protect_content"]; present {
		if raw != "true" && raw != "false" {
			return options, errors.New("PROTECT_CONTENT_INVALID")
		}
		options.ProtectContent = raw == "true"
	}
	var err error
	options.EffectID, err = apiInt64(values["message_effect_id"])
	if err != nil {
		return options, errors.New("EFFECT_ID_INVALID")
	}
	options.ReplyParameters, err = parseBotAPIReplyParameters(values)
	if err != nil {
		return options, err
	}
	if reply := options.ReplyParameters; reply != nil {
		if reply.EphemeralMessageID != 0 || (options.BusinessConnectionID != "" && reply.ChatID != "") {
			return options, errors.New("REPLY_PARAMETERS_INVALID")
		}
	}
	return options, nil
}

func botAPIFileInput(raw string, files map[string]uploadedFile, field string, values map[string]string) (domain.BotAPIFileInput, bool) {
	locationKey, remoteURL, fileName, mimeType, fileBytes, ok := mediaInput(raw, files, field)
	if !ok {
		return domain.BotAPIFileInput{}, false
	}
	result := domain.BotAPIFileInput{
		LocationKey: locationKey, RemoteURL: remoteURL, FileName: fileName, MimeType: mimeType, Bytes: fileBytes,
		Title: values["title"], Performer: values["performer"], Emoji: values["emoji"],
	}
	for field, output := range map[string]*int{"width": &result.Width, "height": &result.Height, "duration": &result.Duration} {
		if raw, present := values[field]; present {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return domain.BotAPIFileInput{}, false
			}
			*output = n
		}
	}
	if raw, present := values["length"]; present {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return domain.BotAPIFileInput{}, false
		}
		result.Width, result.Height = n, n
	}
	for field, output := range map[string]*bool{
		"has_spoiler": &result.HasSpoiler, "show_caption_above_media": &result.ShowCaptionAboveMedia,
		"supports_streaming": &result.SupportsStreaming, "disable_content_type_detection": &result.DisableContentTypeDetection,
	} {
		if raw, present := values[field]; present {
			if raw != "true" && raw != "false" {
				return domain.BotAPIFileInput{}, false
			}
			*output = raw == "true"
		}
	}
	if raw, present := values["start_timestamp"]; present {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return domain.BotAPIFileInput{}, false
		}
		result.StartTimestamp = value
	}
	for field, output := range map[string]**domain.BotAPIFileInput{"thumbnail": &result.Thumbnail, "cover": &result.VideoCover} {
		if raw := values[field]; raw != "" {
			file, ok := botAPIFileInput(raw, files, field, nil)
			if !ok || (field == "thumbnail" && len(file.Bytes) == 0) {
				return domain.BotAPIFileInput{}, false
			}
			*output = &file
		}
	}
	return result, true
}

func (h *handler) writeEphemeralMessage(w http.ResponseWriter, r *http.Request, botID int64, message domain.EphemeralMessage) {
	users := make([]domain.User, 0, 1)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	projected, ok := apiEphemeralMessage(message, users, nil)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	writeAPIOK(w, projected)
}

func (h *handler) sendEphemeralContact(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if strings.TrimSpace(values["phone_number"]) == "" || strings.TrimSpace(values["first_name"]) == "" || len(values["vcard"]) > 2048 {
		writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
		return
	}
	h.sendDirectMedia(w, r, botID, values, "contact", &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{
		PhoneNumber: values["phone_number"], FirstName: values["first_name"], LastName: values["last_name"], Vcard: values["vcard"],
	}})
}

func (h *handler) sendEphemeralLocation(w http.ResponseWriter, r *http.Request, botID int64, venue bool) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	latitude, latErr := strconv.ParseFloat(strings.TrimSpace(values["latitude"]), 64)
	longitude, longErr := strconv.ParseFloat(strings.TrimSpace(values["longitude"]), 64)
	accuracy, accuracyErr := strconv.ParseFloat(defaultString(values["horizontal_accuracy"], "0"), 64)
	if latErr != nil || longErr != nil || accuracyErr != nil || math.IsNaN(latitude) || math.IsNaN(longitude) || math.IsNaN(accuracy) || math.IsInf(latitude, 0) || math.IsInf(longitude, 0) || math.IsInf(accuracy, 0) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 || accuracy < 0 || accuracy > 1500 {
		writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
		return
	}
	geo := domain.MessageGeoPoint{Lat: latitude, Long: longitude, AccuracyRadius: int(accuracy)}
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &geo}
	if venue {
		if strings.TrimSpace(values["title"]) == "" || strings.TrimSpace(values["address"]) == "" {
			writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
			return
		}
		provider, venueID, venueType := "", "", ""
		if values["foursquare_id"] != "" || values["foursquare_type"] != "" {
			provider, venueID, venueType = "foursquare", values["foursquare_id"], values["foursquare_type"]
		} else if values["google_place_id"] != "" || values["google_place_type"] != "" {
			provider, venueID, venueType = "gplaces", values["google_place_id"], values["google_place_type"]
		}
		media = &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{
			Geo: geo, Title: values["title"], Address: values["address"], Provider: provider, VenueID: venueID, VenueType: venueType,
		}}
	}
	if !venue {
		period, heading, radius := 0, 0, 0
		for field, output := range map[string]*int{"live_period": &period, "heading": &heading, "proximity_alert_radius": &radius} {
			if raw, present := values[field]; present {
				value, err := strconv.Atoi(raw)
				if err != nil {
					writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
					return
				}
				*output = value
			}
		}
		if (period != 0 && period != 2147483647 && (period < 60 || period > 86400)) || heading < 0 || heading > 360 || radius < 0 || radius > 100000 || (period == 0 && (heading != 0 || radius != 0)) {
			writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
			return
		}
		if period != 0 {
			media = &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: geo, Period: period, Heading: heading, ProximityNotificationRadius: radius}}
		}
	}
	h.sendDirectMedia(w, r, botID, values, "location", media)
}

func (h *handler) sendDirectMedia(w http.ResponseWriter, r *http.Request, botID int64, values map[string]string, kind string, media *domain.MessageMedia) {
	if h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	target, ephemeral, err := parseEphemeralSendTarget(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	chatID, description := h.ephemeralChatID(r.Context(), botID, values["chat_id"], "MEDIA_INVALID")
	if description != "" {
		writeAPIError(w, http.StatusBadRequest, description)
		return
	}
	var markup *domain.MessageReplyMarkup
	if raw := values["reply_markup"]; raw != "" {
		markup, err = replyMarkupFromAPI(json.RawMessage(raw))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	input := domain.BotAPIEphemeralSendInput{
		BotUserID: botID, ChatID: chatID, ReceiverUserID: target.receiverUserID,
		CallbackQueryID: target.callbackQueryID, ReplaceCallbackQueryMessage: target.replaceCallbackQueryMessage,
		ReplyToEphemeralID: target.replyToEphemeralID, TopMessageID: target.topMessageID,
		Kind: kind, ReplyMarkup: markup, DirectMedia: media,
	}
	if ephemeral {
		if markup != nil && !markup.IsZero() && markup.Kind() != domain.MessageReplyMarkupInline {
			writeAPIError(w, http.StatusBadRequest, "BUTTON_TYPE_INVALID")
			return
		}
		gateway, ok := h.gateway.(EphemeralGatewayService)
		if !ok {
			writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
			return
		}
		message, err := gateway.BotAPISendEphemeral(r.Context(), input)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
			return
		}
		h.writeEphemeralMessage(w, r, botID, message)
		return
	}
	options, err := botAPIMessageOptions(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if options.ReplyParameters != nil {
		input.ReplyToMessageID = options.ReplyParameters.MessageID
	}
	input.Silent, input.ProtectContent = apiBool(values["disable_notification"]), options.ProtectContent
	input.EffectID = options.EffectID
	message, err := h.gateway.BotAPISendOrdinary(r.Context(), input, options)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(message, users))
}

func (h *handler) editEphemeralMessage(w http.ResponseWriter, r *http.Request, botID int64, mode string) {
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	receiverID, receiverErr := strconv.ParseInt(strings.TrimSpace(values["receiver_user_id"]), 10, 64)
	messageID := apiInt(values["ephemeral_message_id"], 0)
	if receiverErr != nil || receiverID <= 0 || messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "EPHEMERAL_MESSAGE_ID_INVALID")
		return
	}
	chatID, description := h.ephemeralChatID(r.Context(), botID, values["chat_id"], "EPHEMERAL_MESSAGE_ID_INVALID")
	if description != "" {
		writeAPIError(w, http.StatusBadRequest, description)
		return
	}
	input := domain.BotAPIEphemeralEditInput{
		BotUserID: botID, ChatID: chatID, ReceiverUserID: receiverID, MessageID: messageID,
		Mode: domain.EphemeralEditMode(mode),
	}
	markup, markupSet, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.Fields.SetReplyMarkup, input.Fields.ReplyMarkup = markupSet, markup
	switch mode {
	case "text":
		text, entities, err := botAPIFormattedTextRaw(values["text"], values["parse_mode"], values["entities"], domain.MaxMessageTextLength, true)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Fields.SetMessage, input.Fields.Message, input.Fields.Entities = true, text, entities
	case "caption":
		caption, entities, err := botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], domain.MaxEphemeralCaptionLength, false)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		input.Fields.SetMessage, input.Fields.Message, input.Fields.Entities = true, caption, entities
	case "reply_markup":
		input.Fields.SetReplyMarkup = true
	case "media":
		if err := parseEphemeralEditMedia(values["media"], &input, files); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		writeAPIError(w, http.StatusNotFound, "METHOD_NOT_FOUND")
		return
	}
	gateway, ok := h.gateway.(EphemeralGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	result, err := gateway.BotAPIEditEphemeral(r.Context(), input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func parseEphemeralEditMedia(raw string, input *domain.BotAPIEphemeralEditInput, files map[string]uploadedFile) error {
	var fields map[string]json.RawMessage
	if input == nil || json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return errors.New("MEDIA_INVALID")
	}
	allowedFields := map[string]bool{
		"type": true, "media": true, "photo": true, "caption": true, "parse_mode": true, "caption_entities": true,
		"width": true, "height": true, "duration": true, "title": true, "performer": true, "thumbnail": true,
		"has_spoiler": true, "show_caption_above_media": true, "supports_streaming": true,
		"disable_content_type_detection": true, "cover": true, "start_timestamp": true,
	}
	values := make(map[string]string, len(fields))
	for field, value := range fields {
		if !allowedFields[field] || string(value) == "null" {
			return errors.New("MEDIA_INVALID")
		}
		switch field {
		case "type", "media", "photo", "caption", "parse_mode", "title", "performer", "thumbnail", "cover":
			if len(value) == 0 || value[0] != '"' {
				return errors.New("MEDIA_INVALID")
			}
		}
		if len(value) != 0 && value[0] == '"' {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return errors.New("MEDIA_INVALID")
			}
			values[field] = text
		} else {
			values[field] = string(value)
		}
	}
	kind := values["type"]
	if !map[string]bool{"animation": true, "audio": true, "document": true, "live_photo": true, "photo": true, "video": true}[kind] {
		return errors.New("MEDIA_INVALID")
	}
	primaryRaw := values["media"]
	if kind == "live_photo" {
		primaryRaw = values["photo"]
	}
	primary, ok := botAPIFileInput(primaryRaw, files, "", values)
	if !ok {
		return errors.New("FILE_ID_INVALID")
	}
	input.MediaKind, input.File = kind, primary
	if kind == "live_photo" {
		secondary, ok := botAPIFileInput(values["media"], files, "", values)
		if !ok || secondary.RemoteURL != "" {
			return errors.New("FILE_ID_INVALID")
		}
		input.SecondaryFile = secondary
	}
	caption, entities, err := botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], domain.MaxEphemeralCaptionLength, false)
	if err != nil {
		return err
	}
	input.Fields.SetMessage, input.Fields.Message, input.Fields.Entities = true, caption, entities
	return nil
}

func (h *handler) deleteEphemeralMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	receiverID, receiverErr := strconv.ParseInt(strings.TrimSpace(values["receiver_user_id"]), 10, 64)
	messageID := apiInt(values["ephemeral_message_id"], 0)
	if receiverErr != nil || receiverID <= 0 || messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "EPHEMERAL_MESSAGE_ID_INVALID")
		return
	}
	chatID, description := h.ephemeralChatID(r.Context(), botID, values["chat_id"], "EPHEMERAL_MESSAGE_ID_INVALID")
	if description != "" {
		writeAPIError(w, http.StatusBadRequest, description)
		return
	}
	gateway, ok := h.gateway.(EphemeralGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	result, err := gateway.BotAPIDeleteEphemeral(r.Context(), botID, chatID, receiverID, messageID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

func optionalInlineReplyMarkup(values map[string]string) (*domain.MessageReplyMarkup, bool, error) {
	raw, exists := values["reply_markup"]
	if !exists || strings.TrimSpace(raw) == "" {
		return nil, exists, nil
	}
	markup, err := inlineReplyMarkupFromAPI(json.RawMessage(raw))
	return markup, true, err
}

// ephemeralChatID 解析 chat_id：格式错误沿用方法原有的错误描述 invalid，
// @username 的解析错误（如 USERNAME_NOT_OCCUPIED）原样透出。
func (h *handler) ephemeralChatID(ctx context.Context, botID int64, raw, invalid string) (int64, string) {
	chatID, err := h.chatIDParam(ctx, botID, raw)
	if err == nil {
		return chatID, ""
	}
	if description := apiErrorDescription(err); description != "CHAT_ID_INVALID" {
		return 0, description
	}
	return 0, invalid
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
