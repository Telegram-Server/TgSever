package memory_test

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestBotUserMenuButtonsMemory(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550007001", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550007002", FirstName: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := users.Create(ctx, domain.User{AccessHash: 3, Phone: "15550007003", FirstName: "B"})
	if err != nil {
		t.Fatal(err)
	}
	bots := memory.NewBotStore(users)
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{AccessHash: 4, FirstName: "MenuBot", Username: "menu_scope_bot"}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	storetest.BotUserMenuButtons(t, bots, users, bot.ID, a.ID, b.ID, nil)
}
