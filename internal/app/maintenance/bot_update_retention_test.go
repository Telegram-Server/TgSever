package maintenance

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

type fakeBotUpdateRetention struct {
	calls  int
	before time.Time
	limit  int
}

func (f *fakeBotUpdateRetention) DeleteExpiredBotUpdates(_ context.Context, before time.Time, limit int) (int, error) {
	f.calls++
	f.before = before
	f.limit = limit
	return 3, nil
}

// bot qts 序列按自己的保留期回收（#145），与 durable update log 的保留期无关。
func TestRetentionWorkerReclaimsBotUpdatesByRetention(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention time.Duration
		want      time.Duration
	}{
		{"configured", 6 * time.Hour, 6 * time.Hour},
		{"defaults to 24h", 0, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeBotUpdateRetention{}
			w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), 7*24*time.Hour, time.Hour, 55).
				WithBotUpdateRetention(store, tc.retention)
			before := time.Now()
			w.runOnce(context.Background())
			after := time.Now()
			if store.calls != 1 || store.limit != 55 {
				t.Fatalf("bot update retention calls/limit = %d/%d, want 1/55", store.calls, store.limit)
			}
			if store.before.Before(before.Add(-tc.want)) || store.before.After(after.Add(-tc.want)) {
				t.Fatalf("bot update cutoff = %v, want now-%v", store.before, tc.want)
			}
		})
	}
}
