package domain

// ValidateReplyMarkupMedia binds game buttons to messages carrying a real game.
func ValidateReplyMarkupMedia(markup *MessageReplyMarkup, media *MessageMedia) error {
	if markup == nil || media != nil && media.Game != nil {
		return nil
	}
	for _, row := range markup.Inline {
		for _, button := range row {
			if button.Type == MarkupButtonGame {
				return ErrButtonTypeInvalid
			}
		}
	}
	return nil
}
