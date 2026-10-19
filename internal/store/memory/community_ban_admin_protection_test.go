package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestCommunityBanAdminProtection(t *testing.T) {
	channels := memory.NewChannelStore()
	communities := memory.NewCommunityStore(memory.NewUserStore(), channels, nil, nil)
	storetest.CommunityBanAdminProtection(t, communities, channels, [5]int64{401, 402, 403, 404, 405})
}
