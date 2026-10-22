package memory

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotForumMemory(t *testing.T) {
	s := NewChannelStore()
	storetest.BotForum(t, s, func() store.ChannelStore { return s }, []int64{101, 102, 103, 104})
}
