package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelMemberPagerContractMemory(t *testing.T) {
	storetest.ChannelMemberPagerContract(t, func(t *testing.T) storetest.ChannelMemberPagerFixture {
		return storetest.ChannelMemberPagerFixture{
			Channels: memory.NewChannelStore(),
			Users:    []int64{1050, 1010, 1040, 1020, 1030, 1060},
		}
	})
}
