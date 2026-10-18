package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// messageDeletionPageLimit 是单次读取删除记录的上限。
const messageDeletionPageLimit = 200

// MessageDeletionStore 读取 message_deletions；写入发生在各删除事务内部。
type MessageDeletionStore struct {
	q *sqlcgen.Queries
}

func NewMessageDeletionStore(db sqlcgen.DBTX) *MessageDeletionStore {
	return &MessageDeletionStore{q: sqlcgen.New(db)}
}

func (s *MessageDeletionStore) ListMessageDeletions(ctx context.Context, filter domain.MessageDeletionFilter) ([]domain.MessageDeletion, error) {
	limit := filter.Limit
	if limit <= 0 || limit > messageDeletionPageLimit {
		limit = messageDeletionPageLimit
	}
	rows, err := s.q.ListMessageDeletions(ctx, sqlcgen.ListMessageDeletionsParams{
		ActorUserID:  filter.ActorUserID,
		SenderUserID: filter.SenderUserID,
		RowLimit:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list message deletions: %w", err)
	}
	out := make([]domain.MessageDeletion, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.MessageDeletion{
			ID:           row.ID,
			BatchID:      row.BatchID,
			Date:         int(row.DeletedDate),
			Source:       domain.DeletionSource(row.Source),
			Scope:        domain.DeletionScope(row.Scope),
			ActorType:    domain.DeletionActor(row.ActorType),
			ActorUserID:  row.ActorUserID,
			OwnerUserID:  row.OwnerUserID,
			Peer:         domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
			MessageID:    int(row.MessageID),
			SenderUserID: row.SenderUserID,
			MessageDate:  int(row.MessageDate),
			Body:         row.Body,

			OriginAuthKeyID: authKeyIDFromInt64(row.OriginAuthKeyID),
			OriginSessionID: row.OriginSessionID,
			OperatorID:      row.OperatorID,
			CommandID:       row.CommandID,
			Reason:          row.Reason,
			OriginDevice: domain.MessageDeletionDevice{
				DeviceModel: row.OriginDeviceModel,
				Platform:    row.OriginPlatform,
				IP:          row.OriginIp,
			},
		})
	}
	return out, nil
}

// insertPrivateMessageDeletionsTx 在删除事务内为被删的 message box 写删除记录。
// 必须在软删除之后、事务提交之前调用：快照取自同一事务内的 message_boxes 行。
func insertPrivateMessageDeletionsTx(ctx context.Context, q *sqlcgen.Queries, audit domain.DeletionAudit, date int, scope domain.DeletionScope, rows []deletedBox) error {
	if len(rows) == 0 {
		return nil
	}
	ownerIDs := make([]int64, 0, len(rows))
	boxIDs := make([]int32, 0, len(rows))
	for _, row := range rows {
		ownerIDs = append(ownerIDs, row.ownerUserID)
		boxIDs = append(boxIDs, int32(row.boxID))
	}
	if err := q.InsertPrivateMessageDeletions(ctx, sqlcgen.InsertPrivateMessageDeletionsParams{
		DeletedDate:     int32(date),
		Source:          string(audit.Source),
		Scope:           string(scope),
		ActorType:       string(audit.ActorType),
		ActorUserID:     audit.ActorUserID,
		OriginAuthKeyID: authKeyIDToInt64(audit.OriginAuthKeyID),
		OriginSessionID: audit.OriginSessionID,
		OperatorID:      audit.OperatorID,
		CommandID:       audit.CommandID,
		Reason:          audit.Reason,
		OwnerUserIds:    ownerIDs,
		BoxIds:          boxIDs,
	}); err != nil {
		return fmt.Errorf("insert private message deletions: %w", err)
	}
	return nil
}
