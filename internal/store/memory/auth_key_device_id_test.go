package memory

import (
	"testing"

	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
)

func TestAuthKeyDeviceIDContract(t *testing.T) {
	var current *AuthKeyStore
	storetest.AuthKeyDeviceIDContract(t,
		func(*testing.T) store.AuthKeyStore {
			current = NewAuthKeyStore()
			return current
		},
		// 内存实现没有"重启"可言，reopen 返回同一个实例。
		func(*testing.T) store.AuthKeyStore { return current },
	)
}
