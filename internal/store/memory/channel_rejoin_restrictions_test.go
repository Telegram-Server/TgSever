package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelRejoinKeepsRestrictions(t *testing.T) {
	storetest.ChannelRejoinKeepsRestrictions(t, memory.NewChannelStore(), [4]int64{401, 402, 403, 404})
}
