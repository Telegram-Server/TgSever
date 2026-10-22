package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var _ store.BotBroadcastStore = (*BotCommerceStore)(nil)

func broadcastTime(v time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: v, Valid: !v.IsZero()}
}
func lockBroadcastWallet(ctx context.Context, q *sqlcgen.Queries, botID int64) (domain.StarsAmount, error) {
	if err := q.EnsureBotBroadcastWallet(ctx, botID); err != nil {
		return domain.StarsAmount{}, err
	}
	row, err := q.LockBotBroadcastWallet(ctx, botID)
	return domain.StarsAmount{Amount: row.Balance, Nanos: row.Nanos}, err
}
func setBroadcastWallet(ctx context.Context, q *sqlcgen.Queries, botID int64, v domain.StarsAmount) error {
	return q.SetBotBroadcastBalance(ctx, sqlcgen.SetBotBroadcastBalanceParams{BotID: botID, Amount: v.Amount, Nanos: v.Nanos})
}
func getBroadcastReservation(ctx context.Context, q *sqlcgen.Queries, botID int64, key string) (domain.BotBroadcastReservation, error) {
	data, err := q.GetBotBroadcastReservation(ctx, sqlcgen.GetBotBroadcastReservationParams{BotID: botID, RequestKey: key})
	if err != nil {
		return domain.BotBroadcastReservation{}, err
	}
	var v domain.BotBroadcastReservation
	err = json.Unmarshal(data, &v)
	return v, err
}
func putBroadcastReservation(ctx context.Context, q *sqlcgen.Queries, v domain.BotBroadcastReservation) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return q.PutBotBroadcastReservation(ctx, sqlcgen.PutBotBroadcastReservationParams{BotID: v.Request.BotID, RequestKey: v.Request.Key, State: v.State, CreatedAt: broadcastTime(v.Request.Now), CompletedAt: broadcastTime(v.CompletedAt), Data: data})
}
func releaseBroadcastReservation(ctx context.Context, q *sqlcgen.Queries, v domain.BotBroadcastReservation, balance *domain.StarsAmount) error {
	if v.State != "reserved" {
		return nil
	}
	next, err := balance.Add(v.Fee)
	if err != nil {
		return err
	}
	if err = setBroadcastWallet(ctx, q, v.Request.BotID, next); err != nil {
		return err
	}
	*balance = next
	v.State = "released"
	return putBroadcastReservation(ctx, q, v)
}
func reclaimExpiredBroadcasts(ctx context.Context, q *sqlcgen.Queries, botID int64, now time.Time, balance *domain.StarsAmount) error {
	rows, err := q.ExpiredBotBroadcastReservations(ctx, sqlcgen.ExpiredBotBroadcastReservationsParams{BotID: botID, Deadline: broadcastTime(now.Add(-domain.BotBroadcastReservationTTL))})
	if err != nil {
		return err
	}
	for _, data := range rows {
		var v domain.BotBroadcastReservation
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		if err := releaseBroadcastReservation(ctx, q, v, balance); err != nil {
			return err
		}
	}
	return nil
}
func (s *BotCommerceStore) BotStarsBalanceExact(ctx context.Context, botID int64) (domain.StarsAmount, error) {
	if botID <= 0 {
		return domain.StarsAmount{}, domain.ErrCommerceInvalid
	}
	var out domain.StarsAmount
	err := withTx(ctx, s.db, "read exact bot wallet", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		balance, err := lockBroadcastWallet(ctx, q, botID)
		if err != nil {
			return err
		}
		if err := reclaimExpiredBroadcasts(ctx, q, botID, time.Now(), &balance); err != nil {
			return err
		}
		out = balance
		return nil
	})
	return out, err
}
func (s *BotCommerceStore) ReserveBotBroadcast(ctx context.Context, req domain.BotBroadcastRequest) (domain.BotBroadcastReservation, error) {
	if !req.Valid() {
		return domain.BotBroadcastReservation{}, domain.ErrBotBroadcastReservation
	}
	_, found, err := NewBotStore(s.db).GetBot(ctx, req.BotID)
	if err != nil {
		return domain.BotBroadcastReservation{}, err
	}
	if !found {
		return domain.BotBroadcastReservation{}, domain.ErrBotNotFound
	}
	var out domain.BotBroadcastReservation
	err = withTx(ctx, s.db, "reserve bot broadcast", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		balance, err := lockBroadcastWallet(ctx, q, req.BotID)
		if err != nil {
			return err
		}
		rows, err := q.ActiveBotBroadcastReservations(ctx, sqlcgen.ActiveBotBroadcastReservationsParams{BotID: req.BotID, Since: broadcastTime(req.Now.Add(-time.Second))})
		if err != nil {
			return err
		}
		count, free := 0, 0
		for _, data := range rows {
			var v domain.BotBroadcastReservation
			if err = json.Unmarshal(data, &v); err != nil {
				return err
			}
			if v.State == "reserved" && !v.Request.Now.Add(domain.BotBroadcastReservationTTL).After(req.Now) {
				if err = releaseBroadcastReservation(ctx, q, v, &balance); err != nil {
					return err
				}
				continue
			}
			if v.CountsAt(req.Now) {
				count++
				if v.Fee.IsZero() {
					free++
				}
			}
		}
		existing, err := getBroadcastReservation(ctx, q, req.BotID, req.Key)
		if err == nil {
			if existing.Request.AllowPaid != req.AllowPaid {
				return domain.ErrBotBroadcastReservation
			}
			if existing.State == "committed" {
				out = existing
				return nil
			}
			if existing.State == "reserved" {
				return domain.ErrBotBroadcastInProgress
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if count >= domain.BotBroadcastPaidLimit {
			return domain.ErrBotBroadcastLimit
		}
		out = domain.BotBroadcastReservation{Request: req, State: "reserved"}
		if free >= domain.BotBroadcastFreeLimit {
			if !req.AllowPaid {
				return domain.ErrBotBroadcastLimit
			}
			enabled, err := q.BotPaidBroadcastEnabled(ctx, req.BotID)
			if err != nil {
				return err
			}
			if !enabled {
				return domain.ErrBotBroadcastNotEnabled
			}
			if balance.Less(domain.StarsAmount{Amount: domain.BotBroadcastMinimumStars}) {
				return domain.ErrBotBroadcastEligibility
			}
			out.Fee = domain.StarsAmount{Nanos: 100_000_000}
			balance, err = balance.Sub(out.Fee)
			if err != nil {
				return err
			}
			if err = setBroadcastWallet(ctx, q, req.BotID, balance); err != nil {
				return err
			}
		}
		return putBroadcastReservation(ctx, q, out)
	})
	return out, err
}
func (s *BotCommerceStore) ReleaseBotBroadcast(ctx context.Context, req domain.BotBroadcastRequest) error {
	return withTx(ctx, s.db, "release bot broadcast", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		balance, err := lockBroadcastWallet(ctx, q, req.BotID)
		if err != nil {
			return err
		}
		v, err := getBroadcastReservation(ctx, q, req.BotID, req.Key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !v.Request.Now.Equal(req.Now) {
			return nil
		}
		return releaseBroadcastReservation(ctx, q, v, &balance)
	})
}
func (s *BotCommerceStore) SetBotPaidBroadcast(ctx context.Context, ownerID, botID int64, enabled bool, date int) error {
	if ownerID <= 0 || botID <= 0 || date <= 0 {
		return domain.ErrCommerceInvalid
	}
	return withTx(ctx, s.db, "set paid broadcast", func(tx pgx.Tx) error {
		profile, found, err := NewBotStore(tx).GetBot(ctx, botID)
		if err != nil {
			return err
		}
		if !found || profile.OwnerUserID != ownerID {
			return domain.ErrBotNotFound
		}
		q := sqlcgen.New(tx)
		balance, err := lockBroadcastWallet(ctx, q, botID)
		if err != nil {
			return err
		}
		if err := reclaimExpiredBroadcasts(ctx, q, botID, time.Unix(int64(date), 0), &balance); err != nil {
			return err
		}
		if enabled && balance.Less(domain.StarsAmount{Amount: domain.BotBroadcastMinimumStars}) {
			return domain.ErrBotBroadcastEligibility
		}
		return q.SetBotPaidBroadcastEnabled(ctx, sqlcgen.SetBotPaidBroadcastEnabledParams{BotID: botID, Enabled: enabled})
	})
}

// Called before the message/outbox transaction commits. A crash cannot expose
// delivery without the matching fee journal or charge a failed/rolled-back send.
func commitBotBroadcastTx(ctx context.Context, tx pgx.Tx, msg *domain.Message, channel *domain.ChannelMessage, duplicate bool) error {
	bc, ok := store.BotBroadcastFromContext(ctx)
	if !ok {
		return nil
	}
	if _, ok := bc.Store.(*BotCommerceStore); !ok || bc.Now == nil {
		return domain.ErrBotBroadcastReservation
	}
	q := sqlcgen.New(tx)
	balance, err := lockBroadcastWallet(ctx, q, bc.Request.BotID)
	if err != nil {
		return err
	}
	v, err := getBroadcastReservation(ctx, q, bc.Request.BotID, bc.Request.Key)
	if err != nil {
		return err
	}
	now := bc.Now()
	if !v.Request.Now.Equal(bc.Request.Now) || v.State != "reserved" || !v.Request.Now.Add(domain.BotBroadcastReservationTTL).After(now) {
		return domain.ErrBotBroadcastReservation
	}
	if duplicate {
		balance, err = balance.Add(v.Fee)
		if err != nil {
			return err
		}
		if err = setBroadcastWallet(ctx, q, bc.Request.BotID, balance); err != nil {
			return err
		}
		v.Fee = domain.StarsAmount{}
	}
	v.State = "committed"
	v.CompletedAt = now
	v.Message = msg
	v.ChannelMessage = channel
	if !v.Fee.IsZero() {
		if err = q.InsertBotBroadcastTransaction(ctx, sqlcgen.InsertBotBroadcastTransactionParams{BotID: bc.Request.BotID, Amount: -v.Fee.Amount, Nanos: -v.Fee.Nanos, RequestKey: "broadcast:" + bc.Request.Key, Date: int32(now.Unix())}); err != nil {
			return err
		}
	}
	return putBroadcastReservation(ctx, q, v)
}
