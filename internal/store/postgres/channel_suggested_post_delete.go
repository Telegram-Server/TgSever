package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PostgreSQL advisory transaction locks are a pgx-only primitive. Every escrow
// command takes this same lock before row locks, so deleting a channel cannot
// miss an approval being committed concurrently or race a lifecycle payout.
func lockSuggestedPostMonoforumTx(ctx context.Context, tx pgx.Tx, monoID int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('suggested-post-monoforum:' || $1::bigint::text, 0))`, monoID)
	return err
}

func (s *ChannelStore) finishDeletedChannelSuggestedPostsTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, date int) error {
	monoID := channel.LinkedMonoforumID
	if channel.Monoforum {
		monoID = channel.ID
	}
	if monoID == 0 {
		return nil
	}
	if date <= 0 {
		date = nowUnix()
	}
	if err := lockSuggestedPostMonoforumTx(ctx, tx, monoID); err != nil {
		return err
	}
	ids, err := sqlcgen.New(tx).ListPendingSuggestedPostIDs(ctx, monoID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	mono, err := getChannelByID(ctx, tx, monoID)
	if err != nil {
		return err
	}
	parent, err := getChannelByID(ctx, tx, mono.LinkedMonoforumID)
	if err != nil {
		return err
	}
	if !mono.Monoforum || !parent.Broadcast || parent.LinkedMonoforumID != mono.ID {
		return domain.ErrSuggestedPostInvalid
	}
	for len(ids) > 0 {
		for _, id := range ids {
			row, found, err := loadSuggestedPostApprovalTx(ctx, tx, mono.ID, int(id), true)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("suggested post deletion invariant: missing approval %d/%d", mono.ID, id)
			}
			action := domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund}
			refund := true
			if row.state == domain.SuggestedPostStatePublished {
				if row.settlementDue <= 0 || row.publishedMessageID <= 0 || row.price == nil {
					return domain.ErrSuggestedPostInvalid
				}
				published, err := sqlcgen.New(tx).GetSuggestedPostPublishedDeletion(ctx, sqlcgen.GetSuggestedPostPublishedDeletionParams{ChannelID: row.parentID, MessageID: int32(row.publishedMessageID)})
				if err != nil {
					return err
				}
				refund = date < row.settlementDue || (published.Deleted && (published.DeleteDate == 0 || int(published.DeleteDate) < row.settlementDue))
			}
			if !refund {
				if row.price == nil {
					return domain.ErrSuggestedPostInvalid
				}
				if err := settleSuggestedPostPaymentTx(ctx, tx, row.actorID, row.payerID, row.parentID, row.price, date); err != nil {
					return err
				}
				received := cloneSuggestedPricePG(row.price)
				received.Amount = received.Amount * paidMessageChannelCommissionPermille / 1000
				action.Type, action.SuggestedPostPrice = domain.ChannelActionSuggestedPostSuccess, received
				row.state = domain.SuggestedPostStateCompleted
			} else {
				if _, _, err := refundSuggestedPostPaymentTx(ctx, tx, row.payerID, row.parentID, row.price, date); err != nil {
					return err
				}
				row.state = domain.SuggestedPostStateRefunded
			}
			if row.price != nil {
				service, _, err := s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, row.actorID, true, row.savedPeer(), row.messageID, date, action)
				if err != nil {
					return err
				}
				row.finalServiceID = service.ID
			}
			if err := upsertSuggestedPostApprovalTx(ctx, tx, row, date); err != nil {
				return err
			}
		}
		ids, err = sqlcgen.New(tx).ListPendingSuggestedPostIDs(ctx, mono.ID)
		if err != nil {
			return err
		}
	}
	return nil
}
