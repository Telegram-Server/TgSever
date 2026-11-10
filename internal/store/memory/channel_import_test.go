package memory_test

import (
	"sync/atomic"
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

var channelImportFixtureSeq atomic.Int64

func TestChannelImport(t *testing.T) {
	storetest.ChannelImport(t, func(t *testing.T) storetest.ChannelImportFixture {
		channels := memory.NewChannelStore()
		base := 300 + channelImportFixtureSeq.Add(1)*10
		return storetest.ChannelImportFixture{
			Channels: channels,
			Imports:  channels,
			Owner:    base + 1,
			Reader:   base + 2,
		}
	})
}
