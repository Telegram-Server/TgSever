package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestSendMonoforumMessageKeepsAlbumGroupedID(t *testing.T) {
	storetest.MonoforumAlbumGrouping(t, memory.NewChannelStore(), 1, 42, "DM album")
}
