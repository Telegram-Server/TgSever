package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// countChannelPostForwardTx 在转发写入的事务里给源帖子的转发数原子加一。源消息不是帖子、
// 已删除或不存在时不计数，也不报错：转发本身已经通过了源消息的可见性校验。
// 行锁只持有到转发事务提交，所以放在事务末尾写。
func countChannelPostForwardTx(ctx context.Context, tx pgx.Tx, ref *domain.ChannelPostRef) error {
	if ref == nil || ref.ChannelID == 0 || ref.MessageID <= 0 || ref.MessageID > domain.MaxMessageBoxID {
		return nil
	}
	if err := sqlcgen.New(tx).IncrementChannelPostForwards(ctx, sqlcgen.IncrementChannelPostForwardsParams{
		ChannelID: ref.ChannelID,
		ID:        int32(ref.MessageID),
	}); err != nil {
		return fmt.Errorf("count channel post forward: %w", err)
	}
	return nil
}
