package memory

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBusinessConnectionLifecycle(t *testing.T) {
	storetest.BusinessConnections(t, NewPasswordStore(), 1001, 1002, 1003)
}
