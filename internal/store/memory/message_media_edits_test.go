package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestMessageMediaEdits(t *testing.T) {
	storetest.MessageMediaEdits(t, func(t *testing.T) storetest.MessageMediaEditFixture {
		return storetest.MessageMediaEditFixture{Messages: memory.NewMessageStore(memory.NewDialogStore()), Sender: 101, Recipient: 102}
	})
}
