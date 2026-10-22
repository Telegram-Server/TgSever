package memory_test

import (
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelMemberTags(t *testing.T) {
	storetest.ChannelMemberTags(t, memory.NewChannelStore(), 7561, 7562)
}

func TestChannelMessageRanks(t *testing.T) {
	storetest.ChannelMessageRanks(t, memory.NewChannelStore(), 7561, 7562)
}
