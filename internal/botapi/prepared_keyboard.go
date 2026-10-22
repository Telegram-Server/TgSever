package botapi

import (
	"encoding/json"
	"errors"
	"math"

	"telesrv/internal/domain"
)

func preparedKeyboardButtonFromAPI(raw string) (domain.BotRequestedWebViewButton, error) {
	var shape map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &shape) != nil || shape == nil {
		return domain.BotRequestedWebViewButton{}, errors.New("BUTTON_INVALID")
	}
	if managed, ok := shape["request_managed_bot"]; ok {
		var p struct {
			RequestID         *int   `json:"request_id"`
			SuggestedName     string `json:"suggested_name"`
			SuggestedUsername string `json:"suggested_username"`
		}
		var text, style, icon string
		if json.Unmarshal(managed, &p) != nil || p.RequestID == nil || *p.RequestID < math.MinInt32 || *p.RequestID > math.MaxInt32 || json.Unmarshal(shape["text"], &text) != nil {
			return domain.BotRequestedWebViewButton{}, errors.New("BUTTON_INVALID")
		}
		for key := range shape {
			switch key {
			case "text", "style", "icon_custom_emoji_id", "request_managed_bot":
			default:
				return domain.BotRequestedWebViewButton{}, errors.New("BUTTON_TYPE_INVALID")
			}
		}
		if value, ok := shape["style"]; ok {
			if json.Unmarshal(value, &style) != nil {
				return domain.BotRequestedWebViewButton{}, errors.New("BUTTON_INVALID")
			}
		}
		value, iconSet := shape["icon_custom_emoji_id"]
		if iconSet && json.Unmarshal(value, &icon) != nil {
			return domain.BotRequestedWebViewButton{}, errors.New("BUTTON_INVALID")
		}
		decoration, emoji, err := markupButtonDecorationFromAPI(style, icon, iconSet)
		if err != nil {
			return domain.BotRequestedWebViewButton{}, err
		}
		return domain.BotRequestedWebViewButton{ButtonID: *p.RequestID, Text: text, PeerType: "managed_bot", MaxQuantity: 1, SuggestedName: p.SuggestedName, SuggestedUsername: p.SuggestedUsername, Style: decoration, IconCustomEmojiID: emoji}, nil
	}
	var p apiKeyboardButton
	if json.Unmarshal([]byte(raw), &p) != nil || p.Unsupported {
		return domain.BotRequestedWebViewButton{}, errors.New("BUTTON_INVALID")
	}
	style, icon, err := markupButtonDecorationFromAPI(p.Style, p.IconCustomEmojiID, p.IconCustomEmojiIDSet)
	if err != nil {
		return domain.BotRequestedWebViewButton{}, err
	}
	out := domain.BotRequestedWebViewButton{ButtonID: p.RequestID, Text: p.Text, Style: style, IconCustomEmojiID: icon, PeerFilter: p.RequestPeerFilter, UsernameRequested: p.RequestUsername, PhotoRequested: p.RequestPhoto}
	switch p.Kind {
	case "request_users":
		out.PeerType, out.MaxQuantity, out.NameRequested = "user", p.MaxQuantity, p.RequestName
	case "request_chat":
		out.PeerType = "chat"
		if p.ChatIsChannel {
			out.PeerType = "broadcast"
		}
		out.MaxQuantity, out.NameRequested = 1, p.RequestTitle
	default:
		return out, errors.New("BUTTON_TYPE_INVALID")
	}
	if out.ButtonID < math.MinInt32 || out.ButtonID > math.MaxInt32 {
		return out, errors.New("BUTTON_INVALID")
	}
	return out, nil
}

func jsonObjectHasField(raw json.RawMessage, key string) bool {
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil {
		return false
	}
	_, ok := shape[key]
	return ok
}
