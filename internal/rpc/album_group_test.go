package rpc

import "testing"

// 官方服务端分配的 grouped_id 都是正数；TDLib 把负数留给本地待发送相册，
// telegram-bot-api 又把它原样输出为 media_group_id，负值会被客户端误判。
func TestRandomPositiveInt64IsAlwaysPositive(t *testing.T) {
	for i := 0; i < 4096; i++ {
		if v := randomPositiveInt64(); v <= 0 {
			t.Fatalf("randomPositiveInt64() = %d at iteration %d, want > 0", v, i)
		}
	}
}
