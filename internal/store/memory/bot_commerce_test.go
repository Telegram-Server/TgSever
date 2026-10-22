package memory

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotCommerceMemory(t *testing.T) {
	storetest.BotCommerce(t, func(t *testing.T) storetest.BotCommerceFixture {
		ctx := context.Background()
		users := NewUserStore()
		create := func(name string) domain.User {
			user, err := users.Create(ctx, domain.User{Phone: name, FirstName: name, AccessHash: 123})
			if err != nil {
				t.Fatal(err)
			}
			return user
		}
		buyer, stranger := create("commercebuyer"), create("commercestranger")
		messages := NewMessageStore()
		stars := NewStarsStore()
		bots := NewBotStore(users)
		bot, _, err := bots.CreateBotAccount(ctx, domain.User{FirstName: "Commerce bot", Username: "commercebot", AccessHash: 234}, domain.BotProfile{OwnerUserID: buyer.ID, TokenSecret: "commerce-secret"})
		if err != nil {
			t.Fatal(err)
		}
		channels := NewChannelStore()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: bot.ID, Title: "Paid channel", Broadcast: true, Date: 1800000000})
		if err != nil {
			t.Fatal(err)
		}
		commerce := NewBotCommerceStore(stars, bots, messages, channels, NewBotAPIUpdateStore())
		return storetest.BotCommerceFixture{Commerce: commerce, Payments: messages, Stars: stars, Channels: channels, BotID: bot.ID, BuyerID: buyer.ID, StrangerID: stranger.ID, ChannelID: created.Channel.ID}
	})
}
