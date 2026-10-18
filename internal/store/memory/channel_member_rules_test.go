package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelMemberPreviousStates(t *testing.T) {
	storetest.ChannelMemberPreviousStates(t, memory.NewChannelStore(), [5]int64{301, 302, 303, 304, 305})
}

func TestChannelAdminProtection(t *testing.T) {
	storetest.ChannelAdminProtection(t, memory.NewChannelStore(), [4]int64{201, 202, 203, 204})
}
