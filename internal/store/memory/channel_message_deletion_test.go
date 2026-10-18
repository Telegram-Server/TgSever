package memory_test

import (
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelMessageDeletions(t *testing.T) {
	deletions := memory.NewMessageDeletionStore()
	channels := memory.NewChannelStore()
	channels.AttachDeletionStore(deletions)
	storetest.ChannelMessageDeletions(t, channels, deletions, 101, 102)
}
