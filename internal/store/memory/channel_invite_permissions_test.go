package memory_test

import (
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelInvitePermissions(t *testing.T) {
	storetest.ChannelInvitePermissions(t, memory.NewChannelStore(), [5]int64{7143, 7144, 7145, 7146, 7147})
}

func TestChannelInviteParameterValidation(t *testing.T) {
	storetest.ChannelInviteParameterValidation(t, memory.NewChannelStore(), 7148)
}
