package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelBannedSenderChats(t *testing.T) {
	storetest.ChannelBannedSenderChats(t, memory.NewChannelStore(), [4]int64{411, 412, 413, 414})
}
