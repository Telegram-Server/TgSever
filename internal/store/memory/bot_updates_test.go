package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestBotUpdatesContract(t *testing.T) {
	storetest.BotUpdatesContract(t, func(t *testing.T) storetest.BotUpdatesFixture {
		users := NewUserStore()
		updates := NewBotUpdateStore(users)
		channels := NewChannelStore()
		channels.AttachBotUpdateStore(updates)
		return storetest.BotUpdatesFixture{
			Updates:  updates,
			Channels: channels,
			NewUser: func(t *testing.T, bot bool) int64 {
				t.Helper()
				u, err := users.Create(context.Background(), domain.User{AccessHash: 77, FirstName: "qts", Bot: bot})
				if err != nil {
					t.Fatalf("create user: %v", err)
				}
				return u.ID
			},
		}
	})
}
