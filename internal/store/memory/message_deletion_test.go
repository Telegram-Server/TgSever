package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestMessageDeletions(t *testing.T) {
	storetest.MessageDeletions(t, func(t *testing.T) storetest.MessageDeletionFixture {
		deletions := memory.NewMessageDeletionStore()
		messages := memory.NewMessageStore(memory.NewDialogStore())
		messages.AttachDeletionStore(deletions)
		return storetest.MessageDeletionFixture{Messages: messages, Deletions: deletions, Sender: 101, Recipient: 102}
	})
}
