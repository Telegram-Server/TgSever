package domain

import (
	"bytes"
	"slices"
	"strings"
	"unicode/utf8"
)

// BotAPIReplySourceText validates typed task/option selection against the actual source.
func BotAPIReplySourceText(reply *MessageReply, source Message) (string, []MessageEntity, error) {
	if reply.ChecklistTaskID != 0 && len(reply.PollOption) != 0 {
		return "", nil, ErrReplyMessageIDInvalid
	}
	if reply.ChecklistTaskID != 0 {
		if source.Media != nil && source.Media.Todo != nil {
			for _, task := range source.Media.Todo.Items {
				if task.ID == reply.ChecklistTaskID {
					return task.Title, task.Entities, nil
				}
			}
		}
		return "", nil, ErrReplyMessageIDInvalid
	}
	if len(reply.PollOption) != 0 {
		if source.Media != nil && source.Media.Poll != nil {
			for _, answer := range source.Media.Poll.Answers {
				if bytes.Equal(answer.Option, reply.PollOption) {
					return answer.Text, answer.Entities, nil
				}
			}
		}
		return "", nil, ErrReplyMessageIDInvalid
	}
	return source.Body, source.Entities, nil
}

// ValidateBotAPIReplySource rechecks the actual text and exact formatting at commit.
func ValidateBotAPIReplySource(reply *MessageReply, source Message) error {
	if reply == nil {
		return nil
	}
	text, entities, err := BotAPIReplySourceText(reply, source)
	if err != nil {
		return err
	}
	if !utf8.ValidString(reply.QuoteText) || utf8.RuneCountInString(reply.QuoteText) > MaxMessageReplyQuoteLength {
		return ErrQuoteTextInvalid
	}
	if reply.QuoteText == "" {
		if len(reply.QuoteEntities) != 0 || reply.QuoteOffset != 0 {
			return ErrQuoteTextInvalid
		}
		return nil
	}
	start := reply.QuoteOffset
	if start < 0 {
		return ErrQuoteTextInvalid
	}
	byteStart, units := 0, 0
	for byteStart < len(text) && units < start {
		character, width := utf8.DecodeRuneInString(text[byteStart:])
		byteStart += width
		units++
		if character > 0xffff {
			units++
		}
	}
	if units != start || !strings.HasPrefix(text[byteStart:], reply.QuoteText) {
		return ErrQuoteTextInvalid
	}
	quoteLength := 0
	for _, character := range reply.QuoteText {
		quoteLength++
		if character > 0xffff {
			quoteLength++
		}
	}
	end := start + quoteLength
	expected := make([]MessageEntity, 0, len(entities))
	for _, entity := range entities {
		if entity.Offset >= end || entity.Offset+entity.Length <= start {
			continue
		}
		// Non-formatting auto-detected entities aren't mandatory in a quote. Explicit
		// quote entities still must correspond to a real source entity below.
		switch entity.Type {
		case MessageEntityBold, MessageEntityItalic, MessageEntityUnderline, MessageEntityStrike, MessageEntitySpoiler, MessageEntityCustomEmoji, MessageEntityFormattedDate:
			entity.Length = min(entity.Offset+entity.Length, end) - max(entity.Offset, start)
			entity.Offset = max(entity.Offset, start) - start
			expected = append(expected, entity)
		}
	}
	provided := make([]MessageEntity, 0, len(reply.QuoteEntities))
	for _, entity := range reply.QuoteEntities {
		if entity.Offset < 0 || entity.Length <= 0 || entity.Offset > quoteLength || entity.Length > quoteLength-entity.Offset {
			return ErrQuoteTextInvalid
		}
		switch entity.Type {
		case MessageEntityBold, MessageEntityItalic, MessageEntityUnderline, MessageEntityStrike, MessageEntitySpoiler, MessageEntityCustomEmoji, MessageEntityFormattedDate:
			provided = append(provided, entity)
		default:
			found := false
			for _, original := range entities {
				original.Length = min(original.Offset+original.Length, end) - max(original.Offset, start)
				original.Offset = max(original.Offset, start) - start
				if original == entity {
					found = true
					break
				}
			}
			if !found {
				return ErrQuoteTextInvalid
			}
		}
	}
	if len(provided) != len(expected) {
		return ErrQuoteTextInvalid
	}
	for _, entity := range expected {
		index := slices.Index(provided, entity)
		if index < 0 {
			return ErrQuoteTextInvalid
		}
		provided = append(provided[:index], provided[index+1:]...)
	}
	return nil
}
