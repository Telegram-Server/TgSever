package memory

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelStickerSetsMemory(t *testing.T) {
	s := NewChannelStore()
	ids := make([]int64, 150)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	storetest.ChannelStickerSets(t, s, func() store.ChannelStore { return s }, ids)
}
