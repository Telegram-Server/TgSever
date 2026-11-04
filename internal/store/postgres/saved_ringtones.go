package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AddSavedRingtone 实现 store.SavedRingtoneStore。上限按插入前的计数判断，同一用户
// 并发新增时可能略超 max；铃声列表只由本人操作，可以接受。
func (s *PasswordStore) AddSavedRingtone(ctx context.Context, userID, documentID int64, max int) (bool, error) {
	n, err := s.q.AddSavedRingtone(ctx, sqlcgen.AddSavedRingtoneParams{UserID: userID, DocumentID: documentID, MaxCount: int64(max)})
	if err != nil {
		return false, fmt.Errorf("add saved ringtone: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	exists, err := s.q.SavedRingtoneExists(ctx, sqlcgen.SavedRingtoneExistsParams{UserID: userID, DocumentID: documentID})
	if err != nil {
		return false, fmt.Errorf("check saved ringtone: %w", err)
	}
	if exists {
		return false, nil
	}
	return false, domain.ErrRingtoneInvalid
}

func (s *PasswordStore) RemoveSavedRingtone(ctx context.Context, userID, documentID int64) (bool, error) {
	n, err := s.q.RemoveSavedRingtone(ctx, sqlcgen.RemoveSavedRingtoneParams{UserID: userID, DocumentID: documentID})
	if err != nil {
		return false, fmt.Errorf("remove saved ringtone: %w", err)
	}
	return n > 0, nil
}

func (s *PasswordStore) ListSavedRingtoneIDs(ctx context.Context, userID int64) ([]int64, error) {
	ids, err := s.q.ListSavedRingtoneIDs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list saved ringtones: %w", err)
	}
	return ids, nil
}
