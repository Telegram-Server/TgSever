package bots

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"unicode/utf8"
)

func WithGamesPassportStore(storage store.BotGamesPassportStore) Option {
	return func(s *Service) { s.gamesPassport = storage }
}
func gameRandomID() int64 {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		id := int64(binary.LittleEndian.Uint64(b[:]) & 0x7fffffffffffffff)
		if id != 0 {
			return id
		}
	}
}
func (s *Service) handleGameBotFather(ctx context.Context, userID int64, msg domain.Message) (botReply, bool) {
	text := strings.TrimSpace(msg.Body)
	if reply, handled := s.handlePassportKeyBotFather(ctx, userID, text); handled {
		return reply, true
	}
	state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
	if err != nil {
		return internalReply(), true
	}
	if text == "/newgame" {
		if s.gamesPassport == nil {
			return internalReply(), true
		}
		if err = s.bots.UpsertBotChatState(ctx, domain.BotChatState{BotUserID: domain.BotFatherUserID, UserID: userID, Command: "newgame", Step: "game_bot", Draft: map[string]string{}}); err != nil {
			return internalReply(), true
		}
		return botReply{Text: "Choose the bot that will offer your game. Send its @username."}, true
	}
	if !found || state.Command != "newgame" || strings.HasPrefix(text, "/cancel") || text == "/help" || text == "/start" {
		return botReply{}, false
	}
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	save := func(step, prompt string) (botReply, bool) {
		state.Step = step
		if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
			return internalReply(), true
		}
		return botReply{Text: prompt}, true
	}
	switch state.Step {
	case "game_bot":
		bot, found, err := s.users.ByUsername(ctx, strings.TrimPrefix(text, "@"))
		if err != nil {
			return internalReply(), true
		}
		if !found {
			return botReply{Text: "Bot not found. Send the @username of a bot you own."}, true
		}
		owns, err := s.OwnsBot(ctx, userID, bot.ID)
		if err != nil {
			return internalReply(), true
		}
		if !owns {
			return botReply{Text: "You can only create games for a bot you own."}, true
		}
		state.Draft["bot_id"] = strconv.FormatInt(bot.ID, 10)
		return save("game_title", "Send the title of your game (1–128 characters).")
	case "game_title":
		if text == "" || utf8.RuneCountInString(text) > 128 {
			return botReply{Text: "The title must contain 1–128 characters."}, true
		}
		state.Draft["title"] = text
		return save("game_description", "Send a description of your game (1–4096 characters).")
	case "game_description":
		if text == "" || utf8.RuneCountInString(text) > 4096 {
			return botReply{Text: "The description must contain 1–4096 characters."}, true
		}
		state.Draft["description"] = text
		return save("game_photo", "Send the photo that will be displayed in your game message.")
	case "game_photo":
		if msg.Media == nil || msg.Media.Photo == nil || msg.Media.Photo.ID == 0 {
			return botReply{Text: "Please send a photo for your game."}, true
		}
		b, err := json.Marshal(msg.Media.Photo)
		if err != nil {
			return internalReply(), true
		}
		state.Draft["photo"] = string(b)
		return save("game_shortname", "Choose a short name for your game (3–64 Latin letters, digits or underscores).")
	case "game_shortname":
		if len(text) < 3 || len(text) > 64 {
			return botReply{Text: "The short name must contain 3–64 Latin letters, digits or underscores."}, true
		}
		for _, c := range text {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
				return botReply{Text: "Use only Latin letters, digits or underscores."}, true
			}
		}
		botID, _ := strconv.ParseInt(state.Draft["bot_id"], 10, 64)
		owns, err := s.OwnsBot(ctx, userID, botID)
		if err != nil {
			return internalReply(), true
		}
		if !owns {
			return botReply{Text: "You no longer own that bot. Use /cancel."}, true
		}
		if _, exists, err := s.gamesPassport.GetBotGame(ctx, botID, text); err != nil {
			return internalReply(), true
		} else if exists {
			return botReply{Text: "That short name is already in use. Choose another."}, true
		}
		game := domain.BotGame{ID: gameRandomID(), AccessHash: gameRandomID(), BotUserID: botID, ShortName: text, Title: state.Draft["title"], Description: state.Draft["description"]}
		if json.Unmarshal([]byte(state.Draft["photo"]), &game.Photo) != nil {
			return internalReply(), true
		}
		if err = s.gamesPassport.CreateBotGame(ctx, game); err != nil {
			return internalReply(), true
		}
		_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
		return botReply{Text: "Success! Your game is ready. Use sendGame with game_short_name: " + text}, true
	}
	return internalReply(), true
}

func (s *Service) handlePassportKeyBotFather(ctx context.Context, userID int64, text string) (botReply, bool) {
	state, found, err := s.bots.GetBotChatState(ctx, domain.BotFatherUserID, userID)
	if err != nil {
		return internalReply(), true
	}
	if text == "/setpassportkey" {
		if s.gamesPassport == nil {
			return internalReply(), true
		}
		err = s.bots.UpsertBotChatState(ctx, domain.BotChatState{BotUserID: domain.BotFatherUserID, UserID: userID, Command: "setpassportkey", Step: "passport_bot", Draft: map[string]string{}})
		if err != nil {
			return internalReply(), true
		}
		return botReply{Text: "Send the @username of the bot whose Telegram Passport public key you want to set."}, true
	}
	if !found || state.Command != "setpassportkey" || text == "/cancel" || text == "/help" || text == "/start" {
		return botReply{}, false
	}
	if state.Step == "passport_bot" {
		bot, found, err := s.users.ByUsername(ctx, strings.TrimPrefix(text, "@"))
		if err != nil {
			return internalReply(), true
		}
		if !found {
			return botReply{Text: "Bot not found."}, true
		}
		owns, err := s.OwnsBot(ctx, userID, bot.ID)
		if err != nil {
			return internalReply(), true
		}
		if !owns {
			return botReply{Text: "You can only set the key for a bot you own."}, true
		}
		state.Step = "passport_key"
		state.Draft = map[string]string{"bot_id": strconv.FormatInt(bot.ID, 10)}
		if err = s.bots.UpsertBotChatState(ctx, state); err != nil {
			return internalReply(), true
		}
		return botReply{Text: "Send the service's RSA public key in PEM format (2048–4096 bits). Never send the private key."}, true
	}
	botID, _ := strconv.ParseInt(state.Draft["bot_id"], 10, 64)
	owns, err := s.OwnsBot(ctx, userID, botID)
	if err != nil {
		return internalReply(), true
	}
	if !owns {
		return botReply{Text: "You no longer own that bot. Use /cancel."}, true
	}
	key, _, err := domain.NormalizePassportPublicKey(text)
	if err != nil {
		return botReply{Text: "Invalid RSA public key. Send a 2048–4096-bit PEM public key."}, true
	}
	if err = s.gamesPassport.SetPassportPublicKey(ctx, botID, key); err != nil {
		return internalReply(), true
	}
	_ = s.bots.DeleteBotChatState(ctx, domain.BotFatherUserID, userID)
	return botReply{Text: "Success! Telegram Passport public key saved."}, true
}
