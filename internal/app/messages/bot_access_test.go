package messages

import (
	"context"
	"errors"
	"testing"

	botsapp "telesrv/internal/app/bots"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestPrivateSendHonorsLiveBotAccessSettings(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{Phone: "15550009801", FirstName: "Owner", AccessHash: 9801})
	if err != nil {
		t.Fatal(err)
	}
	visitor, err := users.Create(ctx, domain.User{Phone: "15550009802", FirstName: "Visitor", AccessHash: 9802})
	if err != nil {
		t.Fatal(err)
	}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, memory.NewBotStore(users), messages)
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Restricted Bot", "restricted_send_bot")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(messages, dialogs, WithBotAccessChecker(bots, users))
	if err := bots.SetBotAccessSettings(ctx, owner.ID, bot.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	send := func(sender, recipient, randomID int64, body string) error {
		_, err := service.SendPrivateText(ctx, sender, domain.SendPrivateTextRequest{
			SenderUserID: sender, RecipientUserID: recipient, RandomID: randomID,
			Message: body, Date: 1_700_000_000,
		})
		return err
	}
	if err := send(visitor.ID, bot.ID, 1, "denied before allowlist"); !errors.Is(err, domain.ErrBotAccessDenied) {
		t.Fatalf("restricted stranger send = %v", err)
	}
	if err := send(owner.ID, bot.ID, 2, "owner"); err != nil {
		t.Fatalf("owner send: %v", err)
	}
	if err := bots.SetBotAccessSettings(ctx, owner.ID, bot.ID, true, []int64{visitor.ID}); err != nil {
		t.Fatal(err)
	}
	if err := send(visitor.ID, bot.ID, 3, "allowed"); err != nil {
		t.Fatalf("allowlisted send: %v", err)
	}
	if err := bots.SetBotAccessSettings(ctx, owner.ID, bot.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := send(visitor.ID, bot.ID, 4, "denied after revocation"); !errors.Is(err, domain.ErrBotAccessDenied) {
		t.Fatalf("revoked stranger send = %v", err)
	}
	if err := send(visitor.ID, owner.ID, 5, "ordinary user"); err != nil {
		t.Fatalf("non-bot recipient: %v", err)
	}
	source, err := messages.ListByUser(ctx, visitor.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, Limit: 10,
	})
	if err != nil || len(source.Messages) != 1 {
		t.Fatalf("forward source: messages=%v err=%v", source.Messages, err)
	}
	if _, err := service.ForwardPrivateMessages(ctx, visitor.ID, domain.ForwardPrivateMessagesRequest{
		OwnerUserID: visitor.ID, FromPeer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ToUserID: bot.ID, MessageIDs: []int{source.Messages[0].ID}, RandomIDs: []int64{6}, Date: 1_700_000_001,
	}); !errors.Is(err, domain.ErrBotAccessDenied) {
		t.Fatalf("restricted stranger forward = %v", err)
	}
	history, err := messages.ListByUser(ctx, bot.ID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("bot received %d messages, want only owner and allowed sends", len(history.Messages))
	}
	for _, message := range history.Messages {
		if message.Body != "owner" && message.Body != "allowed" {
			t.Fatalf("denied message persisted: %q", message.Body)
		}
	}
}
