package redisstore

import (
	"context"
	"github.com/redis/go-redis/v9"
	"os"
	"telesrv/internal/store/storetest"
	"testing"
	"time"
)

func TestRedisMessageDraftLifecycle(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	seed := time.Now().UnixNano() & 0x3fffffffffffffff
	t.Cleanup(func() {
		for _, scope := range [][3]int64{{seed + 1, seed + 2, 0}, {seed + 1, seed + 3, 0}, {seed + 11, seed + 2, 0}, {seed + 1, seed + 2, 17}} {
			key := messageDraftRedisKey(scope[0], scope[1], int(scope[2]))
			keys := []string{key}
			for _, id := range []int64{1<<60 + 7, 1<<60 + 8, 1<<60 + 9, 1<<60 + 27} {
				keys = append(keys, messageDraftStoppedKey(key, id))
			}
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	storetest.MessageDraftLifecycle(t, NewMessageDraftStore(client), seed)
}
