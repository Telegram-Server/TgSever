package bots

import (
	"context"
	"errors"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func WithGuestQueries(queries store.BotGuestQueryStore) Option {
	return func(s *Service) { s.guestQueries = queries }
}

// SetGuestChat is owner-only; enabling inline mode never enables guest access.
func (s *Service) SetGuestChat(ctx context.Context, ownerUserID, botID int64, enabled bool) error {
	profile, found, err := s.botProfile(ctx, botID)
	if err != nil {
		return err
	}
	if !found || profile.OwnerUserID != ownerUserID {
		return domain.ErrBotNotFound
	}
	if s.guestQueries == nil {
		return errors.New("guest query store unavailable")
	}
	if err := s.guestQueries.SetGuestBotEnabled(ctx, botID, enabled); err != nil {
		return err
	}
	s.invalidateBotReadCaches(ctx, botID)
	return nil
}

func (s *Service) handleGuestBotFather(ctx context.Context, userID int64, text string) (botReply, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return botReply{}, false
	}
	command := strings.Split(strings.ToLower(fields[0]), "@")[0]
	if command == "/setmanager" {
		if len(fields) != 3 || (fields[2] != "on" && fields[2] != "off") {
			return botReply{Text: "Usage: /setmanager @your_bot on|off."}, true
		}
		bot, found, err := s.users.ByUsername(ctx, strings.TrimPrefix(fields[1], "@"))
		if err != nil || !found || !bot.Bot {
			return botReply{Text: "Bot not found."}, true
		}
		if err := s.SetCanManageBots(ctx, userID, bot.ID, fields[2] == "on"); err != nil {
			return botReply{Text: "Could not change bot management. Choose a bot you own."}, true
		}
		return botReply{Text: "Bot management is " + fields[2] + " for @" + bot.Username + "."}, true
	}
	if command != "/setguest" {
		return botReply{}, false
	}
	if len(fields) != 3 || (strings.ToLower(fields[2]) != "enable" && strings.ToLower(fields[2]) != "disable") {
		return botReply{Text: "Usage: /setguest @your_bot enable|disable. Guest bots receive only messages that explicitly mention them."}, true
	}
	bot, found, err := s.users.ByUsername(ctx, strings.TrimPrefix(fields[1], "@"))
	if err != nil || !found || !bot.Bot {
		return botReply{Text: "Bot not found."}, true
	}
	if err := s.SetGuestChat(ctx, userID, bot.ID, strings.EqualFold(fields[2], "enable")); err != nil {
		return botReply{Text: "Could not change guest mode. Choose a bot you own."}, true
	}
	return botReply{Text: "Guest mode " + strings.ToLower(fields[2]) + "d for @" + bot.Username + "."}, true
}
