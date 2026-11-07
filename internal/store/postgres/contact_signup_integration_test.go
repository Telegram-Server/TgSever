package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestContactSignUpNoticeContractPostgres(t *testing.T) {
	storetest.ContactSignUpNoticeContract(t, func(t *testing.T) storetest.ContactSignUpNoticeFixture {
		pool := testPool(t)
		users := NewUserStore(pool)
		messages := NewMessageStore(pool)
		return storetest.ContactSignUpNoticeFixture{
			Notices:  messages,
			Messages: messages,
			Dialogs:  NewDialogStore(pool),
			Events:   NewUpdateEventStore(pool),
			NewUser: func(t *testing.T, bot bool) domain.User {
				id := createBotUpdateTestUser(t, pool, bot)
				u, found, err := users.ByID(context.Background(), id)
				if err != nil || !found {
					t.Fatalf("read created user %d found=%v err=%v", id, found, err)
				}
				return u
			},
			DeleteAccount: func(t *testing.T, userID int64) {
				if _, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "contact sign up contract", time.Now()); err != nil {
					t.Fatalf("delete account: %v", err)
				}
			},
		}
	})
}
