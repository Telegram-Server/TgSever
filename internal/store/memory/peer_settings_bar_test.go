package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestPeerSettingsBarContractMemory(t *testing.T) {
	storetest.PeerSettingsBarContract(t, memory.NewDialogStore(), 3001, 3002)
}
