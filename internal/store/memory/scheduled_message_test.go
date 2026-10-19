package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestScheduledMessagesContract(t *testing.T) {
	storetest.ScheduledMessages(t, memory.NewMessageStore(), 1, 2, 42, 1999000000)
}
