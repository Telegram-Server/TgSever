package domain

import (
	"errors"
	"math"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidBotStoryPeriod(period int) bool {
	return period == 21600 || period == 43200 || period == 86400 || period == 172800
}

func ValidateBotStoryAreas(areas []StoryMediaArea) error {
	counts := make(map[StoryMediaAreaKind]int)
	limits := map[StoryMediaAreaKind]int{StoryMediaAreaGeoPoint: 10, StoryMediaAreaSuggestedReaction: 5, StoryMediaAreaURL: 3, StoryMediaAreaWeather: 3, StoryMediaAreaStarGift: 1}
	invalid := errors.New("STORY_AREA_INVALID")
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	percent := func(v float64, positive bool) bool { return finite(v) && v >= 0 && v <= 100 && (!positive || v > 0) }
	for _, area := range areas {
		counts[area.Kind]++
		if counts[area.Kind] > limits[area.Kind] {
			return invalid
		}
		p := area.Coordinates
		if !percent(p.X, false) || !percent(p.Y, false) || !percent(p.W, true) || !percent(p.H, true) || !finite(p.Rotation) || p.Rotation < 0 || p.Rotation > 360 || !percent(p.Radius, false) {
			return invalid
		}
		switch area.Kind {
		case StoryMediaAreaGeoPoint:
			if area.Geo == nil || !finite(area.Geo.Lat) || !finite(area.Geo.Long) || area.Geo.Lat < -90 || area.Geo.Lat > 90 || area.Geo.Long < -180 || area.Geo.Long > 180 {
				return invalid
			}
			if a := area.GeoAddress; a != nil {
				if len(a.CountryISO2) != 2 || a.CountryISO2[0] < 'A' || a.CountryISO2[0] > 'Z' || a.CountryISO2[1] < 'A' || a.CountryISO2[1] > 'Z' {
					return invalid
				}
				for _, part := range []string{a.State, a.City, a.Street} {
					if utf8.RuneCountInString(part) > MaxStoryGeoAddressPartLength || strings.ContainsAny(part, "\x00\r\n") {
						return invalid
					}
				}
			}
		case StoryMediaAreaSuggestedReaction:
			if area.Reaction == nil {
				return invalid
			}
			r := area.Reaction
			if r.Type == MessageReactionCustomEmoji {
				if r.DocumentID <= 0 {
					return invalid
				}
			} else if r.Type != MessageReactionEmoji || strings.TrimSpace(r.Emoticon) == "" || len(r.Emoticon) > 64 {
				return invalid
			}
		case StoryMediaAreaURL:
			u, err := url.Parse(area.URL)
			if err != nil || len(area.URL) > MaxStoryMediaAreaURLLength || strings.IndexFunc(area.URL, unicode.IsSpace) >= 0 || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "tg") {
				return invalid
			}
		case StoryMediaAreaWeather:
			if !finite(area.TemperatureC) || strings.TrimSpace(area.WeatherEmoji) == "" || utf8.RuneCountInString(area.WeatherEmoji) > MaxStoryWeatherEmojiLength || int64(area.Color) < 0 || uint64(area.Color) > math.MaxUint32 {
				return invalid
			}
		case StoryMediaAreaStarGift:
			if area.StarGiftSlug == "" || len(area.StarGiftSlug) > MaxStoryStarGiftSlugLength {
				return invalid
			}
			for _, c := range area.StarGiftSlug {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return invalid
				}
			}
		}
	}
	return nil
}
