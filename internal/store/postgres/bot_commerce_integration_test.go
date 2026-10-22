package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotCommercePostgres(t *testing.T) {
	storetest.BotCommerce(t, func(t *testing.T) storetest.BotCommerceFixture {
		pool := testPool(t)
		ctx := context.Background()
		users := NewUserStore(pool)
		suffix := randomSuffix(t)
		create := func(phone, name string) domain.User {
			user, err := users.Create(ctx, domain.User{Phone: phone + suffix, FirstName: name, AccessHash: 123})
			if err != nil {
				t.Fatal(err)
			}
			return user
		}
		buyer, stranger := create("+18002", "Buyer"), create("+18003", "Stranger")
		bot, _, err := NewBotStore(pool).CreateBotAccount(ctx, domain.User{FirstName: "Commerce bot", Username: "commerce" + suffix + "bot", AccessHash: 234}, domain.BotProfile{OwnerUserID: buyer.ID, TokenSecret: "commerce-" + suffix})
		if err != nil {
			t.Fatal(err)
		}
		messages := NewMessageStore(pool)
		stars := NewStarsStore(pool)
		channels := NewChannelStore(pool)
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: bot.ID, Title: "Paid channel", Broadcast: true, Date: 1800000000})
		if err != nil {
			t.Fatal(err)
		}
		commerce := NewBotCommerceStore(pool)
		return storetest.BotCommerceFixture{Commerce: commerce, Payments: messages, Stars: stars, Channels: channels, BotID: bot.ID, BuyerID: buyer.ID, StrangerID: stranger.ID, ChannelID: created.Channel.ID}
	})
}
