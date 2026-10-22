package memory

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChatJoinQueriesMemory(t *testing.T) {
	s := NewChannelStore()
	storetest.ChatJoinQueries(t, s, func() store.ChannelStore { return s }, 101, 102, 103, 104)
}
