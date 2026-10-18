package memory_test

import (
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestMessageDeletions(t *testing.T) {
	deletions := memory.NewMessageDeletionStore()
	messages := memory.NewMessageStore(memory.NewDialogStore())
	messages.AttachDeletionStore(deletions)
	storetest.MessageDeletions(t, messages, deletions, 101, 102)
}
