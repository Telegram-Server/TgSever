package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelAdminProtection(t *testing.T) {
	storetest.ChannelAdminProtection(t, memory.NewChannelStore(), [4]int64{201, 202, 203, 204})
}
