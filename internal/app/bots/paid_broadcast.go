package bots

import (
	"context"
	"errors"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func WithPaidBroadcast(st store.BotBroadcastStore) Option {
	return func(s *Service) { s.paidBroadcast = st }
}

func (s *Service) SetPaidBroadcastEnabled(ctx context.Context, ownerID, botID int64, enabled bool) error {
	if s.paidBroadcast == nil {
		return errors.New("paid broadcast store unavailable")
	}
	return s.paidBroadcast.SetBotPaidBroadcast(ctx, ownerID, botID, enabled, int(s.now().Unix()))
}
func (s *Service) handlePaidBroadcastBotFather(ctx context.Context, userID int64, text string) (botReply, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || strings.Split(strings.ToLower(fields[0]), "@")[0] != "/paidbroadcast" {
		return botReply{}, false
	}
	if len(fields) != 3 || (!strings.EqualFold(fields[2], "on") && !strings.EqualFold(fields[2], "off")) {
		return botReply{Text: "Usage: /paidbroadcast @your_bot on|off. Paid broadcasts require at least 10,000 Stars in the bot's own balance. The first 30 messages per second are free; additional successful messages cost 0.1 Stars each, up to 1000 per second."}, true
	}
	bot, found, err := s.users.ByUsername(ctx, strings.TrimPrefix(fields[1], "@"))
	if err != nil || !found || !bot.Bot {
		return botReply{Text: "Bot not found."}, true
	}
	err = s.SetPaidBroadcastEnabled(ctx, userID, bot.ID, strings.EqualFold(fields[2], "on"))
	if errors.Is(err, domain.ErrBotBroadcastEligibility) {
		return botReply{Text: "Paid broadcasts require at least 10,000 Stars in the bot's own balance."}, true
	}
	if err != nil {
		return botReply{Text: "Could not change paid broadcasts. Choose a bot you own and try again."}, true
	}
	return botReply{Text: "Paid broadcasts are " + strings.ToLower(fields[2]) + " for @" + bot.Username + "."}, true
}
