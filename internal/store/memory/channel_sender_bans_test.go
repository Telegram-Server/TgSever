package memory_test

import (
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelSenderBans(t *testing.T) {
	s := memory.NewChannelStore()
	storetest.ChannelSenderBans(t, s, func() store.ChannelStore { return s }, 101, 102)
	t.Run("linked discussion auto-forward", func(t *testing.T) { storetest.ChannelSenderBanLinkedDiscussion(t, s, 101) })
}
