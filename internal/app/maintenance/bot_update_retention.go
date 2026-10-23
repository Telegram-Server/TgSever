package maintenance

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// BotUpdateRetentionStore 回收 bot qts 序列（#145）里超过保留期的记录。已分配的 qts 不回退；
// 请求更早 qts 的 bot 由 updates.getDifference 返回 differenceTooLong。
type BotUpdateRetentionStore interface {
	DeleteExpiredBotUpdates(ctx context.Context, before time.Time, limit int) (int, error)
}

// defaultBotUpdateRetention 与官方 Bot API 的更新保留期一致。
const defaultBotUpdateRetention = 24 * time.Hour

// WithBotUpdateRetention 启用 bot qts 序列的回收；retention <=0 时用 24h。
func (w *RetentionWorker) WithBotUpdateRetention(store BotUpdateRetentionStore, retention time.Duration) *RetentionWorker {
	if retention <= 0 {
		retention = defaultBotUpdateRetention
	}
	w.botUpdates = store
	w.botUpdateRetention = retention
	return w
}

func (w *RetentionWorker) runBotUpdateRetention(ctx context.Context) {
	if w.botUpdates == nil {
		return
	}
	deleted, err := w.botUpdates.DeleteExpiredBotUpdates(ctx, time.Now().Add(-w.botUpdateRetention), w.batch)
	if err != nil {
		w.logger.Warn("回收 bot qts 序列失败", zap.Error(err))
	} else if deleted > 0 {
		w.logger.Info("回收 bot qts 序列完成", zap.Int("deleted", deleted))
	}
}
