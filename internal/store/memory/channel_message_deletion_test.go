package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelMessageDeletions(t *testing.T) {
	storetest.ChannelMessageDeletions(t, func(t *testing.T) storetest.ChannelMessageDeletionFixture {
		deletions := memory.NewMessageDeletionStore()
		channels := memory.NewChannelStore()
		channels.AttachDeletionStore(deletions)
		return storetest.ChannelMessageDeletionFixture{Channels: channels, Deletions: deletions, Owner: 101, Member: 102}
	})
}
