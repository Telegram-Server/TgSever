package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestPremiumBoostLists(t *testing.T) {
	storetest.PremiumBoostLists(t, memory.NewChannelStore(), [3]int64{701, 702, 703})
}
