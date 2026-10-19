package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelRestrictNonMember(t *testing.T) {
	storetest.ChannelRestrictNonMember(t, memory.NewChannelStore(), [4]int64{601, 602, 603, 604})
}
