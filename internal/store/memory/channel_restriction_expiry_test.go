package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelRestrictionExpiry(t *testing.T) {
	storetest.ChannelRestrictionExpiry(t, memory.NewChannelStore(), [4]int64{501, 502, 503, 504})
}
