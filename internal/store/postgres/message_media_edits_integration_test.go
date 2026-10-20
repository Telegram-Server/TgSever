package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestMessageMediaEditsPostgres(t *testing.T) {
	pool := testPool(t)
	storetest.MessageMediaEdits(t, func(t *testing.T) storetest.MessageMediaEditFixture {
		ctx := context.Background()
		users := NewUserStore(pool)
		suffix := randomSuffix(t)
		sender, err := users.Create(ctx, domain.User{AccessHash: 1201, Phone: "+179" + suffix + "01", FirstName: "MediaSender"})
		if err != nil {
			t.Fatal(err)
		}
		recipient, err := users.Create(ctx, domain.User{AccessHash: 1202, Phone: "+179" + suffix + "02", FirstName: "MediaRecipient"})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.MessageMediaEditFixture{Messages: NewMessageStore(pool), Sender: sender.ID, Recipient: recipient.ID}
	})
}
