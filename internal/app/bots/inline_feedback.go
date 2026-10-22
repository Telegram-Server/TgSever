package bots

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func WithInlineFeedback(settings store.BotInlineFeedbackStore) Option {
	return func(s *Service) { s.inlineFeedback = settings }
}
func (s *Service) SetInlineFeedback(ctx context.Context, ownerID, botID int64, percentage int) error {
	if percentage < 0 || percentage > 100 {
		return errors.New("PERCENTAGE_INVALID")
	}
	p, found, err := s.botProfile(ctx, botID)
	if err != nil {
		return err
	}
	if !found || p.OwnerUserID != ownerID {
		return domain.ErrBotNotFound
	}
	if s.inlineFeedback == nil {
		return errors.New("inline feedback store unavailable")
	}
	return s.inlineFeedback.SetInlineFeedback(ctx, botID, percentage)
}
func (s *Service) handleInlineFeedbackBotFather(ctx context.Context, userID int64, text string) (botReply, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || strings.Split(strings.ToLower(fields[0]), "@")[0] != "/setinlinefeedback" {
		return botReply{}, false
	}
	if len(fields) != 3 {
		return botReply{Text: "Usage: /setinlinefeedback @your_bot percentage (0 to 100). Use 0 to disable chosen-result feedback."}, true
	}
	percentage, err := strconv.Atoi(fields[2])
	if err != nil || percentage < 0 || percentage > 100 {
		return botReply{Text: "Choose a feedback percentage from 0 to 100."}, true
	}
	bot, found, err := s.users.ByUsername(ctx, strings.TrimPrefix(fields[1], "@"))
	if err != nil || !found || !bot.Bot {
		return botReply{Text: "Bot not found."}, true
	}
	if err := s.SetInlineFeedback(ctx, userID, bot.ID, percentage); err != nil {
		return botReply{Text: "Could not update feedback. Choose a bot you own."}, true
	}
	return botReply{Text: "Chosen-result feedback for @" + bot.Username + " is set to " + strconv.Itoa(percentage) + "%."}, true
}
