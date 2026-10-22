package botapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

const maxBotAPIRichSourceBytes = 256 << 10

func richMessageInputFromAPI(raw string) (domain.BotAPIRichMessageInput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxBotAPIRichSourceBytes {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
	}
	var out domain.BotAPIRichMessageInput
	sources := 0
	if value, ok := fields["html"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, &out.HTML); err != nil || out.HTML == "" {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
		sources++
	}
	if value, ok := fields["markdown"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, &out.Markdown); err != nil || out.Markdown == "" {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
		sources++
	}
	if value, ok := fields["blocks"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if len(bytes.TrimSpace(value)) == 0 {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
		out.BlocksJSON = append([]byte(nil), value...)
		sources++
	}
	if sources != 1 {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
	}
	if len(out.BlocksJSON) != 0 {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_BLOCKS_UNSUPPORTED")
	}
	if value, ok := fields["media"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !bytes.Equal(bytes.TrimSpace(value), []byte("[]")) {
		out.MediaJSON = append([]byte(nil), value...)
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_MEDIA_UNSUPPORTED")
	}
	if value, ok := fields["is_rtl"]; ok {
		if err := json.Unmarshal(value, &out.RTL); err != nil {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
	}
	if value, ok := fields["skip_entity_detection"]; ok {
		if err := json.Unmarshal(value, &out.SkipEntityDetection); err != nil {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
	}
	return out, nil
}

func parseBotAPIReplyParameters(values map[string]string) (*domain.BotAPIReplyParameters, error) {
	invalid := errors.New("REPLY_PARAMETERS_INVALID")
	legacyRaw, legacyPresent := values["reply_to_message_id"]
	raw, present := values["reply_parameters"]
	if !present {
		if !legacyPresent {
			return nil, nil
		}
		id, err := strconv.Atoi(legacyRaw)
		if err != nil || id <= 0 || id > domain.MaxMessageBoxID {
			return nil, invalid
		}
		return &domain.BotAPIReplyParameters{MessageID: id}, nil
	}
	if legacyPresent {
		return nil, invalid
	}
	var payload struct {
		MessageID                int             `json:"message_id"`
		EphemeralMessageID       int             `json:"ephemeral_message_id"`
		ChatID                   json.RawMessage `json:"chat_id"`
		AllowSendingWithoutReply bool            `json:"allow_sending_without_reply"`
		Quote                    string          `json:"quote"`
		QuoteParseMode           string          `json:"quote_parse_mode"`
		QuoteEntities            json.RawMessage `json:"quote_entities"`
		QuotePosition            *int            `json:"quote_position"`
		ChecklistTaskID          *int            `json:"checklist_task_id"`
		PollOptionID             string          `json:"poll_option_id"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF ||
		payload.MessageID < 0 || payload.MessageID > domain.MaxMessageBoxID ||
		payload.EphemeralMessageID < 0 || payload.EphemeralMessageID > domain.MaxMessageBoxID ||
		(payload.MessageID == 0) == (payload.EphemeralMessageID == 0) {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return nil, invalid
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalid
		}
	}
	result := &domain.BotAPIReplyParameters{
		MessageID: payload.MessageID, EphemeralMessageID: payload.EphemeralMessageID,
		AllowSendingWithoutReply: payload.AllowSendingWithoutReply,
		QuotePosition:            payload.QuotePosition, PollOptionID: payload.PollOptionID,
	}
	if len(payload.ChatID) != 0 {
		if payload.ChatID[0] == '"' {
			if json.Unmarshal(payload.ChatID, &result.ChatID) != nil {
				return nil, invalid
			}
		} else {
			result.ChatID = string(payload.ChatID)
		}
		if result.ChatID == "" || result.ChatID != strings.TrimSpace(result.ChatID) {
			return nil, invalid
		}
		if strings.HasPrefix(result.ChatID, "@") {
			if len(result.ChatID) == 1 {
				return nil, invalid
			}
		} else if id, err := strconv.ParseInt(result.ChatID, 10, 64); err != nil || id == 0 {
			return nil, invalid
		}
	}
	if result.QuotePosition != nil && (*result.QuotePosition < 0 || *result.QuotePosition > domain.MaxMessageReplyQuoteOffset) {
		return nil, invalid
	}
	if payload.ChecklistTaskID != nil {
		if *payload.ChecklistTaskID <= 0 || *payload.ChecklistTaskID > domain.MaxMessageBoxID {
			return nil, invalid
		}
		result.ChecklistTaskID = *payload.ChecklistTaskID
	}
	if result.ChecklistTaskID != 0 && result.PollOptionID != "" {
		return nil, invalid
	}
	var err error
	result.Quote, result.QuoteEntities, err = botAPIFormattedTextRaw(payload.Quote, payload.QuoteParseMode, string(payload.QuoteEntities), 1024, false)
	if err != nil {
		return nil, err
	}
	if result.Quote == "" && (result.QuotePosition != nil || len(result.QuoteEntities) != 0) {
		return nil, invalid
	}
	return result, nil
}

func parseBotAPISuggestedPost(raw string) (*domain.SuggestedPost, error) {
	invalid := errors.New("SUGGESTED_POST_PARAMETERS_INVALID")
	var parameters struct {
		Price *struct {
			Currency string `json:"currency"`
			Amount   int64  `json:"amount"`
		} `json:"price"`
		SendDate *int `json:"send_date"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&parameters) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return nil, invalid
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalid
		}
	}
	result := &domain.SuggestedPost{}
	if parameters.SendDate != nil {
		if *parameters.SendDate <= 0 || *parameters.SendDate > domain.MaxMessageBoxID {
			return nil, invalid
		}
		result.ScheduleDate = *parameters.SendDate
	}
	if parameters.Price != nil {
		price := parameters.Price
		switch price.Currency {
		case "XTR":
			if price.Amount < 5 || price.Amount > 100000 {
				return nil, invalid
			}
			result.Price = &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: price.Amount}
		case "TON":
			if price.Amount < 10000000 || price.Amount > 10000000000000 {
				return nil, invalid
			}
			result.Price = &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceTON, Amount: price.Amount}
		default:
			return nil, invalid
		}
	}
	return result, nil
}

func apiInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("VALUE_INVALID")
	}
	return value, nil
}
