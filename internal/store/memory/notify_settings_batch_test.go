package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestNotifySettingsBatchContractMemory(t *testing.T) {
	storetest.NotifySettingsBatchContract(t, memory.NewPasswordStore(), [3]int64{1001, 1002, 1003})
}

func TestNotifySoundContractMemory(t *testing.T) {
	storetest.NotifySoundContract(t, memory.NewPasswordStore(), 1004)
}
