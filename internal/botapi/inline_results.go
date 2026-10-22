package botapi

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// Inline results share a message-content override, caption and keyboard across
// cached files, remote media, contacts and locations.
func inlineNonArticleResultFromAPI(raw string, base apiInlineResult) (domain.BotInlineResult, error) {
	var p struct {
		Caption               string          `json:"caption"`
		ParseMode             string          `json:"parse_mode"`
		CaptionEntities       json.RawMessage `json:"caption_entities"`
		ShowCaptionAboveMedia bool            `json:"show_caption_above_media"`
		PhotoURL              string          `json:"photo_url"`
		PhotoFileID           string          `json:"photo_file_id"`
		PhotoWidth            int             `json:"photo_width"`
		PhotoHeight           int             `json:"photo_height"`
		GIFURL                string          `json:"gif_url"`
		GIFFileID             string          `json:"gif_file_id"`
		GIFWidth              int             `json:"gif_width"`
		GIFHeight             int             `json:"gif_height"`
		GIFDuration           int             `json:"gif_duration"`
		MPEG4URL              string          `json:"mpeg4_url"`
		MPEG4FileID           string          `json:"mpeg4_file_id"`
		MPEG4Width            int             `json:"mpeg4_width"`
		MPEG4Height           int             `json:"mpeg4_height"`
		MPEG4Duration         int             `json:"mpeg4_duration"`
		VideoURL              string          `json:"video_url"`
		VideoFileID           string          `json:"video_file_id"`
		VideoWidth            int             `json:"video_width"`
		VideoHeight           int             `json:"video_height"`
		VideoDuration         int             `json:"video_duration"`
		AudioURL              string          `json:"audio_url"`
		AudioFileID           string          `json:"audio_file_id"`
		AudioDuration         int             `json:"audio_duration"`
		Performer             string          `json:"performer"`
		VoiceURL              string          `json:"voice_url"`
		VoiceFileID           string          `json:"voice_file_id"`
		VoiceDuration         int             `json:"voice_duration"`
		DocumentURL           string          `json:"document_url"`
		DocumentFileID        string          `json:"document_file_id"`
		StickerFileID         string          `json:"sticker_file_id"`
		MimeType              string          `json:"mime_type"`
		ThumbnailURL          string          `json:"thumbnail_url"`
		ThumbnailMimeType     string          `json:"thumbnail_mime_type"`
		ThumbnailWidth        int             `json:"thumbnail_width"`
		ThumbnailHeight       int             `json:"thumbnail_height"`
		Latitude              *float64        `json:"latitude"`
		Longitude             *float64        `json:"longitude"`
		HorizontalAccuracy    float64         `json:"horizontal_accuracy"`
		LivePeriod            int             `json:"live_period"`
		Heading               int             `json:"heading"`
		Proximity             int             `json:"proximity_alert_radius"`
		Address               string          `json:"address"`
		FoursquareID          string          `json:"foursquare_id"`
		FoursquareType        string          `json:"foursquare_type"`
		GooglePlaceID         string          `json:"google_place_id"`
		GooglePlaceType       string          `json:"google_place_type"`
		PhoneNumber           string          `json:"phone_number"`
		FirstName             string          `json:"first_name"`
		LastName              string          `json:"last_name"`
		Vcard                 string          `json:"vcard"`
		GameShortName         string          `json:"game_short_name"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil {
		return domain.BotInlineResult{}, errors.New("RESULT_TYPE_INVALID")
	}
	markup, err := inlineReplyMarkupFromAPI(base.ReplyMarkup)
	if err != nil {
		return domain.BotInlineResult{}, err
	}
	out := domain.BotInlineResult{ID: base.ID, Type: base.Type, Title: base.Title, Description: base.Description, URL: base.URL, ReplyMarkup: markup, InvertMedia: p.ShowCaptionAboveMedia}
	if p.ThumbnailURL != "" {
		if !validInlineResultURL(p.ThumbnailURL) {
			return out, errors.New("WEBDOCUMENT_URL_INVALID")
		}
		mime := p.ThumbnailMimeType
		if mime == "" {
			mime = "image/jpeg"
		}
		out.Thumb = &domain.BotInlineWebDocument{URL: p.ThumbnailURL, MimeType: mime, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrImageSize, W: p.ThumbnailWidth, H: p.ThumbnailHeight}}}
	}
	remote, fileID, mime := "", "", p.MimeType
	width, height, duration := 0, 0, 0
	switch base.Type {
	case "photo":
		remote, fileID, mime, width, height = p.PhotoURL, p.PhotoFileID, "image/jpeg", p.PhotoWidth, p.PhotoHeight
	case "gif":
		remote, fileID, mime, width, height, duration = p.GIFURL, p.GIFFileID, "image/gif", p.GIFWidth, p.GIFHeight, p.GIFDuration
	case "mpeg4_gif":
		remote, fileID, mime, width, height, duration = p.MPEG4URL, p.MPEG4FileID, "video/mp4", p.MPEG4Width, p.MPEG4Height, p.MPEG4Duration
		out.Type = "gif"
	case "video":
		remote, fileID, width, height, duration = p.VideoURL, p.VideoFileID, p.VideoWidth, p.VideoHeight, p.VideoDuration
	case "audio":
		remote, fileID, mime, duration = p.AudioURL, p.AudioFileID, "audio/mpeg", p.AudioDuration
	case "voice":
		remote, fileID, mime, duration = p.VoiceURL, p.VoiceFileID, "audio/ogg", p.VoiceDuration
	case "document":
		remote, fileID = p.DocumentURL, p.DocumentFileID
		out.Type = "file"
	case "sticker":
		fileID = p.StickerFileID
	case "location", "venue":
		media, err := inlineLocationMedia(p.Latitude, p.Longitude, p.HorizontalAccuracy, p.LivePeriod, p.Heading, p.Proximity)
		if err != nil {
			return out, err
		}
		out.Media = media
		if base.Type == "venue" {
			if strings.TrimSpace(base.Title) == "" || strings.TrimSpace(p.Address) == "" {
				return out, errors.New("MEDIA_INVALID")
			}
			venue := &domain.MessageVenue{Geo: *media.Geo, Title: base.Title, Address: p.Address}
			if p.FoursquareID != "" {
				venue.Provider, venue.VenueID, venue.VenueType = "foursquare", p.FoursquareID, p.FoursquareType
			} else if p.GooglePlaceID != "" {
				venue.Provider, venue.VenueID, venue.VenueType = "gplaces", p.GooglePlaceID, p.GooglePlaceType
			}
			out.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: venue}
		}
	case "contact":
		if p.PhoneNumber == "" || p.FirstName == "" || len(p.Vcard) > 2048 {
			return out, errors.New("MEDIA_INVALID")
		}
		out.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{PhoneNumber: p.PhoneNumber, FirstName: p.FirstName, LastName: p.LastName, Vcard: p.Vcard}}
	case "game":
		if p.GameShortName == "" {
			return out, errors.New("GAME_SHORT_NAME_INVALID")
		}
		out.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindGame, Game: &domain.BotGame{ShortName: p.GameShortName}}
	default:
		return out, errors.New("RESULT_TYPE_INVALID")
	}
	if width < 0 || height < 0 || duration < 0 || p.ThumbnailWidth < 0 || p.ThumbnailHeight < 0 {
		return out, errors.New("MEDIA_INVALID")
	}
	if remote != "" && fileID != "" {
		return out, errors.New("MEDIA_INVALID")
	}
	if remote != "" {
		if !validInlineResultURL(remote) || mime == "" {
			return out, errors.New("WEBDOCUMENT_INVALID")
		}
		attrs := []domain.DocumentAttribute{}
		switch base.Type {
		case "photo":
			attrs = append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrImageSize, W: width, H: height})
		case "gif", "mpeg4_gif", "video":
			attrs = append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: width, H: height, Duration: float64(duration)})
			if base.Type != "video" {
				attrs = append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrAnimated})
			}
		case "audio", "voice":
			attrs = append(attrs, domain.DocumentAttribute{Kind: domain.DocAttrAudio, Title: base.Title, Performer: p.Performer, AudioDuration: duration, Voice: base.Type == "voice"})
		}
		out.Content = &domain.BotInlineWebDocument{URL: remote, MimeType: mime, Attributes: attrs}
		out.MediaAuto = true
	} else if fileID != "" {
		key, ok := decodeBotAPIFileID(fileID)
		if !ok {
			return out, errors.New("FILE_ID_INVALID")
		}
		parts := strings.Split(key, ":")
		if len(parts) < 2 {
			return out, errors.New("FILE_ID_INVALID")
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return out, errors.New("FILE_ID_INVALID")
		}
		if base.Type == "photo" && parts[0] == "photo" {
			out.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: id}}
		} else if base.Type != "photo" && parts[0] == "doc" {
			out.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: id}}
		} else {
			return out, errors.New("FILE_ID_INVALID")
		}
	} else if out.Media == nil {
		return out, errors.New("MEDIA_INVALID")
	}
	if base.Type != "game" {
		out.Message, out.Entities, err = botAPIFormattedTextRaw(p.Caption, p.ParseMode, string(p.CaptionEntities), domain.MaxEphemeralCaptionLength, false)
		if err != nil {
			return out, err
		}
	}
	if len(base.InputMessageContent) > 0 && string(base.InputMessageContent) != "null" {
		return inlineOverrideContent(out, base)
	}
	return out, nil
}

func inlineOverrideContent(out domain.BotInlineResult, base apiInlineResult) (domain.BotInlineResult, error) {
	var content map[string]json.RawMessage
	if json.Unmarshal(base.InputMessageContent, &content) != nil {
		return out, errors.New("MESSAGE_EMPTY")
	}
	constructors := 0
	for _, key := range []string{"message_text", "rich_message", "currency", "phone_number", "latitude"} {
		if _, ok := content[key]; ok {
			constructors++
		}
	}
	if constructors != 1 {
		return out, errors.New("INPUT_MESSAGE_CONTENT_INVALID")
	}
	if raw, ok := content["rich_message"]; ok {
		input, err := richMessageInputFromAPI(string(raw))
		if err != nil {
			return out, err
		}
		out.RichMessageInput = &input
		out.Message, out.Entities, out.Media, out.MediaAuto = "", nil, nil, false
		return out, nil
	}
	if _, ok := content["currency"]; ok {
		invoice, payload, err := inlineInvoiceContentFromAPI(base.InputMessageContent)
		if err != nil {
			return out, err
		}
		out.Message, out.Entities, out.Media, out.MediaAuto = "", nil, &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice, ProductInvoice: &invoice}, false
		out.InvoicePayload = payload
		return out, nil
	}
	if _, ok := content["message_text"]; ok {
		message, entities, noWebpage, err := inputTextMessageContentFromAPI(base)
		if err != nil {
			return out, err
		}
		out.Message, out.Entities, out.NoWebpage = message, entities, noWebpage
		out.Media, out.MediaAuto = nil, false
		return out, nil
	}
	// Location, venue and contact InputMessageContent use the same fields as their
	// corresponding result types, while preserving the result's picker metadata.
	typ := "location"
	if _, ok := content["phone_number"]; ok {
		typ = "contact"
	} else if _, ok := content["address"]; ok {
		typ = "venue"
	}
	var title string
	if value, ok := content["title"]; ok {
		_ = json.Unmarshal(value, &title)
	}
	raw := map[string]any{}
	for k, v := range content {
		raw[k] = v
	}
	raw["type"], raw["id"], raw["title"] = typ, out.ID, title
	encoded, _ := json.Marshal(raw)
	mediaResult, err := inlineNonArticleResultFromAPI(string(encoded), apiInlineResult{Type: typ, ID: out.ID, Title: title})
	if err != nil {
		return out, err
	}
	out.Message, out.Entities, out.Media, out.MediaAuto = "", nil, mediaResult.Media, false
	return out, nil
}

func inlineLocationMedia(lat, long *float64, accuracy float64, period, heading, proximity int) (*domain.MessageMedia, error) {
	if lat == nil || long == nil || math.IsNaN(*lat) || math.IsInf(*lat, 0) || math.IsNaN(*long) || math.IsInf(*long, 0) || *lat < -90 || *lat > 90 || *long < -180 || *long > 180 || accuracy < 0 || accuracy > 1500 || heading < 0 || heading > 360 || proximity < 0 || proximity > 100000 || (period != 0 && period != math.MaxInt32 && (period < 60 || period > 86400)) {
		return nil, errors.New("GEO_POINT_INVALID")
	}
	geo := domain.MessageGeoPoint{Lat: *lat, Long: *long, AccuracyRadius: int(math.Ceil(accuracy))}
	if period != 0 {
		return &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{Geo: geo, Period: period, Heading: heading, ProximityNotificationRadius: proximity}}, nil
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &geo}, nil
}

func validInlineResultURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= domain.MaxBotInlineWebURLLen && u.Host != "" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") && !strings.ContainsAny(raw, " \t\r\n")
}
