package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestDialogFolderLimitContractMemory(t *testing.T) {
	storetest.DialogFolderLimitContract(t, func(t *testing.T) storetest.DialogFolderLimitFixture {
		return storetest.DialogFolderLimitFixture{Dialogs: memory.NewDialogStore(), UserA: 1001, UserB: 1002}
	})
}
