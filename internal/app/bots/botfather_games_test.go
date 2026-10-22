package bots

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestBotFatherGameRegistrationUsesOwnedBotAndPhoto(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	gp := memory.NewGamesPassportStore()
	svc.gamesPassport = gp
	owner := newOwner(t, users, "+1088")
	bot, _, err := svc.CreateBot(context.Background(), owner.ID, "Racer", "racer_registration_bot")
	if err != nil {
		t.Fatal(err)
	}
	sendToBotFather(t, svc, messages, owner, "/newgame")
	sendToBotFather(t, svc, messages, owner, "@racer_registration_bot")
	sendToBotFather(t, svc, messages, owner, "Race")
	sendToBotFather(t, svc, messages, owner, "Finish first")
	svc.respondAsBotFatherMessage(owner.ID, domain.Message{Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 888, AccessHash: 889, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 640, H: 480, Size: 100}}}}})
	sendToBotFather(t, svc, messages, owner, "race")
	game, found, err := gp.GetBotGame(context.Background(), bot.ID, "race")
	if err != nil || !found || game.Title != "Race" || game.Photo.ID != 888 || game.BotUserID != bot.ID {
		t.Fatalf("registered game=%+v found=%v err=%v", game, found, err)
	}
	stranger := newOwner(t, users, "+1089")
	sendToBotFather(t, svc, messages, stranger, "/newgame")
	sendToBotFather(t, svc, messages, stranger, "@racer_registration_bot")
	state, found, err := svc.bots.GetBotChatState(context.Background(), domain.BotFatherUserID, stranger.ID)
	if err != nil || !found || state.Step != "game_bot" {
		t.Fatalf("stranger advanced owned game registration: %+v %v", state, err)
	}
}
