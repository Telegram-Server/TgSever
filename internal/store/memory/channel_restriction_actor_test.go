package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelRestrictionActor(t *testing.T) {
	storetest.ChannelRestrictionActor(t, memory.NewChannelStore(), [4]int64{611, 612, 613, 614})
}

func TestChannelBannedFilterEachRight(t *testing.T) {
	storetest.ChannelBannedFilterEachRight(t, memory.NewChannelStore(), [2]int64{621, 622})
}
