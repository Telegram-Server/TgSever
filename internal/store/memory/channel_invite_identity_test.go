package memory_test

import (
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelInviteIdentity(t *testing.T) {
	storetest.ChannelInviteIdentity(t, memory.NewChannelStore(), 7142)
}
