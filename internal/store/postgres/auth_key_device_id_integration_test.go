package postgres

import (
	"testing"
	"time"

	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
)

func TestAuthKeyDeviceIDContractPostgres(t *testing.T) {
	pool := testTx(t)
	storetest.AuthKeyDeviceIDContract(t,
		func(*testing.T) store.AuthKeyStore { return NewAuthKeyStore(pool) },
		// 只共享数据库的新实例，模拟服务端重启后重新读取；用生产路径的批量读取包装，
		// 让批量 Get/Revalidate 的列也被覆盖。
		func(t *testing.T) store.AuthKeyStore {
			batched, err := NewBatchedAuthKeyStore(NewAuthKeyStore(pool), AuthKeyGetBatchConfig{
				MaxSize: 8, MaxWait: time.Millisecond, QueueSize: 16, QueryTimeout: 5 * time.Second,
			})
			if err != nil {
				t.Fatalf("new batched auth key store: %v", err)
			}
			t.Cleanup(batched.Close)
			return batched
		},
	)
}
