package store

import (
	"context"

	"telesrv/internal/domain"
)

// MessageDeletionStore 读取消息删除记录；记录由各删除路径在删除事务内写入。
type MessageDeletionStore interface {
	ListMessageDeletions(ctx context.Context, filter domain.MessageDeletionFilter) ([]domain.MessageDeletion, error)
}
