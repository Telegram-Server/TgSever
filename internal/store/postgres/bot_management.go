package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

func (s *BotStore) GetBotManagement(ctx context.Context, botID int64) (domain.BotManagement, error) {
	row, err := s.q.GetBotManagement(ctx, botID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotManagement{}, domain.ErrBotNotFound
	}
	if err != nil {
		return domain.BotManagement{}, err
	}
	return domain.BotManagement{BotUserID: row.BotUserID, ManagerBotID: row.ManagerBotID, IsAccessRestricted: row.IsAccessRestricted, AddedUserIDs: row.AddedUserIds}, nil
}
func (s *BotStore) SetBotManager(ctx context.Context, ownerID, botID, managerID int64) error {
	n, err := s.q.SetBotManager(ctx, sqlcgen.SetBotManagerParams{ManagerID: managerID, BotID: botID, OwnerID: ownerID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrBotManagerInvalid
	}
	return nil
}
func (s *BotStore) SetManagedBotAccess(ctx context.Context, actorID, botID int64, restricted bool, added []int64) error {
	if added == nil {
		added = []int64{}
	}
	n, err := s.q.SetManagedBotAccess(ctx, sqlcgen.SetManagedBotAccessParams{Restricted: restricted, Added: added, BotID: botID, ActorID: actorID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrBotManagerInvalid
	}
	return nil
}
func botInstanceTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}
func (s *BotStore) BeginBotAPIInstance(ctx context.Context, key domain.BotAPIInstanceKey, cloud bool, now time.Time) (domain.BotAPIInstance, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.BotAPIInstance{}, errors.New("bot instance requires transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.BotAPIInstance{}, err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	if _, err = q.LockBotInstanceOwner(ctx, key.BotUserID); errors.Is(err, pgx.ErrNoRows) {
		return domain.BotAPIInstance{}, domain.ErrBotNotFound
	} else if err != nil {
		return domain.BotAPIInstance{}, err
	}
	if cloud {
		unix, err := q.GetBotAPICloudLogout(ctx, key.BotUserID)
		if err != nil {
			return domain.BotAPIInstance{}, err
		}
		until := botInstanceTime(unix).Add(10 * time.Minute)
		if unix > 0 && now.Before(until) {
			return domain.BotAPIInstance{}, &domain.BotAPIInstanceWaitError{Seconds: int(math.Ceil(until.Sub(now).Seconds()))}
		}
	}
	row, err := q.GetBotAPIInstance(ctx, sqlcgen.GetBotAPIInstanceParams{BotUserID: key.BotUserID, InstanceID: key.InstanceID})
	if err == nil {
		v := domain.BotAPIInstance{BotUserID: row.BotUserID, InstanceID: row.InstanceID, Cloud: row.Cloud, StartedAt: botInstanceTime(row.StartedAt), ClosedAt: botInstanceTime(row.ClosedAt), LoggedOutAt: botInstanceTime(row.LoggedOutAt)}
		if !v.ClosedAt.IsZero() {
			return v, domain.ErrBotInstanceClosed
		}
		return v, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.BotAPIInstance{}, err
	}
	v := domain.BotAPIInstance{BotUserID: key.BotUserID, InstanceID: key.InstanceID, Cloud: cloud, StartedAt: now}
	if err = q.InsertBotAPIInstance(ctx, sqlcgen.InsertBotAPIInstanceParams{BotUserID: key.BotUserID, InstanceID: key.InstanceID, Cloud: cloud, NowUnix: now.Unix()}); err != nil {
		return domain.BotAPIInstance{}, err
	}
	return v, tx.Commit(ctx)
}
func (s *BotStore) StopBotAPIInstance(ctx context.Context, key domain.BotAPIInstanceKey, logout bool, now time.Time) error {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return errors.New("bot instance requires transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.q.WithTx(tx)
	if _, err = q.LockBotInstanceOwner(ctx, key.BotUserID); err != nil {
		return err
	}
	row, err := q.GetBotAPIInstance(ctx, sqlcgen.GetBotAPIInstanceParams{BotUserID: key.BotUserID, InstanceID: key.InstanceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrBotInstanceClosed
	}
	if err != nil {
		return err
	}
	if row.ClosedAt != 0 {
		return domain.ErrBotInstanceClosed
	}
	until := time.Unix(row.StartedAt, 0).Add(10 * time.Minute)
	if !logout && now.Before(until) {
		return &domain.BotAPIInstanceWaitError{Seconds: int(math.Ceil(until.Sub(now).Seconds()))}
	}
	if err = q.StopBotAPIInstance(ctx, sqlcgen.StopBotAPIInstanceParams{BotUserID: key.BotUserID, InstanceID: key.InstanceID, Logout: logout, NowUnix: now.Unix()}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *BotStore) SetCanManageBots(ctx context.Context, ownerID, botID int64, enabled bool) error {
	n, err := s.q.SetCanManageBots(ctx, sqlcgen.SetCanManageBotsParams{Enabled: enabled, BotID: botID, OwnerID: ownerID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrBotNotFound
	}
	return nil
}
