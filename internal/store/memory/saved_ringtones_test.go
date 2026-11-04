package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestSavedRingtoneContractMemory(t *testing.T) {
	storetest.SavedRingtoneContract(t, memory.NewPasswordStore(), 2001, 2002, [3]int64{31, 32, 33})
}
