package memory

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestContactSignUpNoticeContract(t *testing.T) {
	var seq atomic.Int64
	storetest.ContactSignUpNoticeContract(t, func(t *testing.T) storetest.ContactSignUpNoticeFixture {
		users := NewUserStore()
		dialogs := NewDialogStore()
		messages := NewMessageStore(dialogs)
		events := NewUpdateEventStore()
		messages.AttachUpdateEventStore(events)
		return storetest.ContactSignUpNoticeFixture{
			Notices:  NewContactSignUpNoticeStore(messages, users),
			Messages: messages,
			Dialogs:  dialogs,
			Events:   events,
			NewUser: func(t *testing.T, bot bool) domain.User {
				u := domain.User{AccessHash: 1, FirstName: "User", Bot: bot}
				if !bot {
					u.Phone = fmt.Sprintf("1555032%04d", seq.Add(1))
				}
				created, err := users.Create(context.Background(), u)
				if err != nil {
					t.Fatalf("create user: %v", err)
				}
				return created
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				if _, err := NewAccountLifecycleStore(users, nil).ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "contact sign up contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
