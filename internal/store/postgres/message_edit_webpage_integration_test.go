package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestEditWebPagePostgres(t *testing.T) {
	pool := testPool(t)
	storetest.EditWebPage(t, func(t *testing.T) storetest.EditWebPageFixture {
		ctx := context.Background()
		users := NewUserStore(pool)
		suffix := randomSuffix(t)
		owner, err := users.Create(ctx, domain.User{AccessHash: 1301, Phone: "+179" + suffix + "01", FirstName: "WebPageOwner"})
		if err != nil {
			t.Fatal(err)
		}
		recipient, err := users.Create(ctx, domain.User{AccessHash: 1302, Phone: "+179" + suffix + "02", FirstName: "WebPageRecipient"})
		if err != nil {
			t.Fatal(err)
		}
		return storetest.EditWebPageFixture{
			Messages:  NewMessageStore(pool),
			Channels:  NewChannelStore(pool),
			Owner:     owner.ID,
			Recipient: recipient.ID,
		}
	})
}
