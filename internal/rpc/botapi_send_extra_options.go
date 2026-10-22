package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) botAPISendOneMediaWithOptions(ctx context.Context, botID, chatID int64, input tg.InputMediaClass, caption string, entities []domain.MessageEntity, markup *domain.MessageReplyMarkup, silent, protect bool, replyID int, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.Message{}, err
	}
	media, err := r.resolveInputMedia(ctx, botID, input)
	if err != nil {
		return domain.Message{}, err
	}
	return r.botAPISendContent(ctx, botID, chatID, caption, entities, media, nil, markup, silent, protect, replyID, 0, options)
}
func (r *Router) BotAPISendDiceWithOptions(ctx context.Context, botID, chatID int64, emoji string, markup *domain.MessageReplyMarkup, silent, protect bool, replyID int, options domain.BotAPIMessageOptions) (domain.Message, error) {
	return r.botAPISendOneMediaWithOptions(ctx, botID, chatID, &tg.InputMediaDice{Emoticon: emoji}, "", nil, markup, silent, protect, replyID, options)
}
func (r *Router) BotAPISendMediaGroupWithOptions(ctx context.Context, botID, chatID int64, items []BotAPIInputMediaItem, silent, protect bool, replyID int, options domain.BotAPIMessageOptions) ([]domain.Message, error) {
	if len(items) < 2 || len(items) > 10 {
		return nil, errors.New("MEDIA_GROUP_INVALID")
	}
	media := make([]*domain.MessageMedia, len(items))
	albumKind := ""
	for index, item := range items {
		category := "visual"
		switch item.Kind {
		case "audio", "document":
			category = item.Kind
		case "photo", "video", "live_photo":
		default:
			return nil, errors.New("MEDIA_GROUP_INVALID")
		}
		if albumKind != "" && albumKind != category {
			return nil, errors.New("MEDIA_GROUP_INVALID")
		}
		albumKind = category
		var err error
		media[index], err = r.botAPIEphemeralMedia(ctx, botID, item.Kind, item.File, item.SecondaryFile)
		if err != nil {
			return nil, err
		}
		media[index].Spoiler = item.Spoiler
	}
	options.AlbumGroupID = randomNonZeroInt64()
	result := make([]domain.Message, 0, len(items))
	for index, item := range items {
		message, err := r.botAPISendContent(ctx, botID, chatID, item.Caption, item.Entities, media[index], nil, nil, silent, protect, replyID, 0, options)
		if err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	return result, nil
}
