package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"telesrv/internal/domain"
)

type StoriesGateway interface {
	BotAPIPostStory(context.Context, int64, domain.BotStoryInput) (domain.Story, error)
	BotAPIRepostStory(context.Context, int64, domain.BotStoryInput) (domain.Story, error)
	BotAPIEditStory(context.Context, int64, domain.BotStoryInput) (domain.Story, error)
	BotAPIDeleteStory(context.Context, int64, string, int) (bool, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"postStory": storyMethod("post"), "repostStory": storyMethod("repost"),
		"editStory": storyMethod("edit"), "deleteStory": storyMethod("delete"),
	})
}

func storyMethod(operation string) methodFunc {
	return func(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
		gateway, ok := h.gateway.(StoriesGateway)
		if !ok {
			writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
			return
		}
		values, files, err := requestValuesWithFiles(r)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
			return
		}
		input, err := storyInputFromAPI(operation, values, files)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		var story domain.Story
		switch operation {
		case "post":
			story, err = gateway.BotAPIPostStory(r.Context(), botID, input)
		case "repost":
			story, err = gateway.BotAPIRepostStory(r.Context(), botID, input)
		case "edit":
			story, err = gateway.BotAPIEditStory(r.Context(), botID, input)
		case "delete":
			var deleted bool
			deleted, err = gateway.BotAPIDeleteStory(r.Context(), botID, input.BusinessConnectionID, input.StoryID)
			if err == nil {
				writeAPIOK(w, deleted)
				return
			}
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
			return
		}
		writeAPIOK(w, map[string]any{"id": story.ID, "chat": map[string]any{"id": story.Owner.ID, "type": "private"}})
	}
}

func storyInputFromAPI(operation string, values map[string]string, files map[string]uploadedFile) (domain.BotStoryInput, error) {
	input := domain.BotStoryInput{BusinessConnectionID: values["business_connection_id"], PostToChatPage: apiBool(values["post_to_chat_page"]), ProtectContent: apiBool(values["protect_content"])}
	if strings.TrimSpace(input.BusinessConnectionID) == "" {
		return input, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	if operation == "edit" || operation == "delete" {
		id, err := strconv.Atoi(values["story_id"])
		if err != nil || id <= 0 || id > domain.MaxStoryID {
			return input, errors.New("STORY_ID_INVALID")
		}
		input.StoryID = id
	}
	if operation == "delete" {
		return input, nil
	}
	if operation == "post" || operation == "repost" {
		period, err := strconv.Atoi(values["active_period"])
		if err != nil || !domain.ValidBotStoryPeriod(period) {
			return input, errors.New("STORY_PERIOD_INVALID")
		}
		input.ActivePeriod = period
	}
	if operation == "repost" {
		var err error
		input.FromChatID, err = strconv.ParseInt(values["from_chat_id"], 10, 64)
		if err != nil || input.FromChatID <= 0 {
			return input, errors.New("CHAT_ID_INVALID")
		}
		input.FromStoryID, err = strconv.Atoi(values["from_story_id"])
		if err != nil || input.FromStoryID <= 0 || input.FromStoryID > domain.MaxStoryID {
			return input, errors.New("STORY_ID_INVALID")
		}
		return input, nil
	}
	var err error
	input.Caption, input.Entities, err = botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], 2048, false)
	if err != nil {
		return input, err
	}
	input.Content, err = storyContentFromAPI(values["content"], files)
	if err != nil {
		return input, err
	}
	input.Areas, err = storyAreasFromAPI(values["areas"])
	return input, err
}

func storyContentFromAPI(raw string, files map[string]uploadedFile) (domain.BotStoryContent, error) {
	var content struct {
		Type      string  `json:"type"`
		Photo     string  `json:"photo"`
		Video     string  `json:"video"`
		Duration  float64 `json:"duration"`
		Cover     float64 `json:"cover_frame_timestamp"`
		Animation bool    `json:"is_animation"`
	}
	invalid := errors.New("STORY_CONTENT_INVALID")
	if json.Unmarshal([]byte(raw), &content) != nil {
		return domain.BotStoryContent{}, invalid
	}
	ref := content.Photo
	limit := 10 << 20
	if content.Type == "video" {
		ref = content.Video
		limit = 30 << 20
	} else if content.Type != "photo" {
		return domain.BotStoryContent{}, invalid
	}
	if !strings.HasPrefix(ref, "attach://") {
		return domain.BotStoryContent{}, errors.New("STORY_CONTENT_MUST_BE_UPLOADED")
	}
	file, ok := files[strings.TrimPrefix(ref, "attach://")]
	if !ok || len(file.Bytes) == 0 || len(file.Bytes) > limit || math.IsNaN(content.Duration) || math.IsInf(content.Duration, 0) || content.Duration < 0 || content.Duration > 60 || math.IsNaN(content.Cover) || math.IsInf(content.Cover, 0) || content.Cover < 0 || content.Cover > 60 {
		return domain.BotStoryContent{}, invalid
	}
	return domain.BotStoryContent{Type: content.Type, File: domain.BotAPIFileInput{Bytes: file.Bytes, FileName: file.Name, MimeType: file.MimeType}, Duration: content.Duration, CoverFrameTimestamp: content.Cover, IsAnimation: content.Animation}, nil
}

func storyAreasFromAPI(raw string) ([]domain.StoryMediaArea, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []struct {
		Position struct {
			X        *float64 `json:"x_percentage"`
			Y        *float64 `json:"y_percentage"`
			W        *float64 `json:"width_percentage"`
			H        *float64 `json:"height_percentage"`
			Rotation *float64 `json:"rotation_angle"`
			Radius   *float64 `json:"corner_radius_percentage"`
		} `json:"position"`
		Type struct {
			Type      string   `json:"type"`
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
			Address   *struct {
				Country string `json:"country_code"`
				State   string `json:"state"`
				City    string `json:"city"`
				Street  string `json:"street"`
			} `json:"address"`
			Reaction *struct {
				Type          string `json:"type"`
				Emoji         string `json:"emoji"`
				CustomEmojiID string `json:"custom_emoji_id"`
			} `json:"reaction_type"`
			Dark        bool     `json:"is_dark"`
			Flipped     bool     `json:"is_flipped"`
			URL         string   `json:"url"`
			Temperature *float64 `json:"temperature"`
			Emoji       string   `json:"emoji"`
			Color       *uint32  `json:"background_color"`
			Name        string   `json:"name"`
		} `json:"type"`
	}
	invalid := errors.New("STORY_AREA_INVALID")
	if json.Unmarshal([]byte(raw), &entries) != nil || strings.TrimSpace(raw) == "null" {
		return nil, invalid
	}
	out := make([]domain.StoryMediaArea, 0, len(entries))
	for _, entry := range entries {
		p, t := entry.Position, entry.Type
		if p.X == nil || p.Y == nil || p.W == nil || p.H == nil || p.Rotation == nil || p.Radius == nil {
			return nil, invalid
		}
		area := domain.StoryMediaArea{Coordinates: domain.StoryMediaAreaCoordinates{X: *p.X, Y: *p.Y, W: *p.W, H: *p.H, Rotation: *p.Rotation, Radius: *p.Radius, HasRadius: true}}
		switch t.Type {
		case "location":
			if t.Latitude == nil || t.Longitude == nil {
				return nil, invalid
			}
			area.Kind = domain.StoryMediaAreaGeoPoint
			area.Geo = &domain.MessageGeoPoint{Lat: *t.Latitude, Long: *t.Longitude}
			if t.Address != nil {
				area.GeoAddress = &domain.StoryGeoPointAddress{CountryISO2: t.Address.Country, State: t.Address.State, City: t.Address.City, Street: t.Address.Street}
			}
		case "suggested_reaction":
			if t.Reaction == nil {
				return nil, invalid
			}
			area.Kind, area.Dark, area.Flipped = domain.StoryMediaAreaSuggestedReaction, t.Dark, t.Flipped
			id, _ := strconv.ParseInt(t.Reaction.CustomEmojiID, 10, 64)
			area.Reaction = &domain.MessageReaction{Type: domain.MessageReactionType(t.Reaction.Type), Emoticon: t.Reaction.Emoji, DocumentID: id}
		case "link":
			area.Kind, area.URL = domain.StoryMediaAreaURL, t.URL
		case "weather":
			if t.Temperature == nil || t.Color == nil {
				return nil, invalid
			}
			area.Kind, area.TemperatureC, area.WeatherEmoji, area.Color = domain.StoryMediaAreaWeather, *t.Temperature, t.Emoji, int(*t.Color)
		case "unique_gift":
			area.Kind, area.StarGiftSlug = domain.StoryMediaAreaStarGift, t.Name
		default:
			return nil, invalid
		}
		out = append(out, area)
	}
	if err := domain.ValidateBotStoryAreas(out); err != nil {
		return nil, err
	}
	return out, nil
}
