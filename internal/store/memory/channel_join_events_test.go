package memory

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelJoinEventSnapshots(t *testing.T) {
	storetest.ChannelJoinEventSnapshots(t, NewChannelStore(), [4]int64{1001, 1002, 1003, 1004})
}
