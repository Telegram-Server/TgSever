package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var _ store.BroadcastScheduleStore = (*BroadcastStore)(nil)

func (s *BroadcastStore) CreateBroadcastSchedule(ctx context.Context, schedule domain.BroadcastSchedule, actor string) (domain.BroadcastSchedule, error) {
	var out domain.BroadcastSchedule
	err := withTx(ctx, s.db, "create broadcast schedule", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		// 计划总数的上限由同一事务里的计数把关；并发新建在上限附近最多多出一两条，可以接受。
		count, err := q.CountBroadcastSchedules(ctx)
		if err != nil {
			return fmt.Errorf("count broadcast schedules: %w", err)
		}
		if count >= domain.MaxBroadcastSchedules {
			return domain.ErrBroadcastScheduleLimit
		}
		row, err := q.InsertBroadcastSchedule(ctx, sqlcgen.InsertBroadcastScheduleParams{
			Message:   schedule.Message,
			Timezone:  schedule.Timezone,
			TimeOfDay: int32(schedule.TimeOfDay),
			Weekdays:  int16(schedule.Weekdays),
			Enabled:   schedule.Enabled,
			NextRunAt: scheduleTimestamptz(schedule.NextRunAt),
			Actor:     actor,
		})
		if err != nil {
			return fmt.Errorf("insert broadcast schedule: %w", err)
		}
		out = broadcastScheduleFromModel(row)
		return nil
	})
	if err != nil {
		return domain.BroadcastSchedule{}, err
	}
	return out, nil
}

func (s *BroadcastStore) UpdateBroadcastSchedule(ctx context.Context, schedule domain.BroadcastSchedule, actor string) (domain.BroadcastSchedule, error) {
	row, err := sqlcgen.New(s.db).UpdateBroadcastSchedule(ctx, sqlcgen.UpdateBroadcastScheduleParams{
		Message:   schedule.Message,
		Timezone:  schedule.Timezone,
		TimeOfDay: int32(schedule.TimeOfDay),
		Weekdays:  int16(schedule.Weekdays),
		Enabled:   schedule.Enabled,
		NextRunAt: scheduleTimestamptz(schedule.NextRunAt),
		Actor:     actor,
		ID:        schedule.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
	}
	if err != nil {
		return domain.BroadcastSchedule{}, fmt.Errorf("update broadcast schedule: %w", err)
	}
	return broadcastScheduleFromModel(row), nil
}

func (s *BroadcastStore) DeleteBroadcastSchedule(ctx context.Context, id int64) error {
	n, err := sqlcgen.New(s.db).DeleteBroadcastSchedule(ctx, id)
	if err != nil {
		return fmt.Errorf("delete broadcast schedule: %w", err)
	}
	if n == 0 {
		return domain.ErrBroadcastScheduleNotFound
	}
	return nil
}

func (s *BroadcastStore) GetBroadcastSchedule(ctx context.Context, id int64) (domain.BroadcastSchedule, error) {
	row, err := sqlcgen.New(s.db).GetBroadcastSchedule(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
	}
	if err != nil {
		return domain.BroadcastSchedule{}, fmt.Errorf("get broadcast schedule: %w", err)
	}
	return broadcastScheduleFromModel(row), nil
}

func (s *BroadcastStore) ListBroadcastSchedules(ctx context.Context, limit int) ([]domain.BroadcastSchedule, error) {
	if limit <= 0 || limit > domain.MaxBroadcastSchedules {
		limit = domain.MaxBroadcastSchedules
	}
	rows, err := sqlcgen.New(s.db).ListBroadcastSchedules(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list broadcast schedules: %w", err)
	}
	out := make([]domain.BroadcastSchedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, broadcastScheduleFromModel(row))
	}
	return out, nil
}

func (s *BroadcastStore) FireDueBroadcastSchedule(ctx context.Context, now time.Time, plan store.BroadcastSchedulePlanner) (store.BroadcastScheduleOutcome, bool, error) {
	if plan == nil {
		return store.BroadcastScheduleOutcome{}, false, domain.ErrBroadcastInvalid
	}
	var (
		outcome store.BroadcastScheduleOutcome
		fired   bool
	)
	err := withTx(ctx, s.db, "fire broadcast schedule", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, err := q.LockDueBroadcastSchedule(ctx, scheduleTimestamptz(now))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock due broadcast schedule: %w", err)
		}
		running, err := q.BroadcastScheduleHasUnfinishedBroadcast(ctx, &row.ID)
		if err != nil {
			return fmt.Errorf("check unfinished scheduled broadcast: %w", err)
		}
		schedule := broadcastScheduleFromModel(row)
		decision := plan(store.DueBroadcastSchedule{Schedule: schedule, PreviousRunning: running})
		fired = true
		outcome = store.BroadcastScheduleOutcome{ScheduleID: schedule.ID, NextRunAt: decision.NextRunAt}

		skip := decision.SkipReason
		if skip == "" {
			snapshot, err := q.SnapshotAllBroadcastRecipients(ctx, domain.SystemUserIDs())
			if err != nil {
				return fmt.Errorf("snapshot scheduled broadcast recipients: %w", err)
			}
			if snapshot.RecipientCount == 0 {
				skip = domain.BroadcastSkipNoRecipients
			} else {
				entitiesJSON, err := encodeMessageEntities(decision.Entities)
				if err != nil {
					return fmt.Errorf("encode scheduled broadcast entities: %w", err)
				}
				scheduleID := schedule.ID
				broadcastID, err := q.InsertScheduledBroadcast(ctx, sqlcgen.InsertScheduledBroadcastParams{
					Message:           schedule.Message,
					Entities:          entitiesJSON,
					SnapshotMaxUserID: snapshot.MaxUserID,
					TargetCount:       snapshot.RecipientCount,
					CreatedBy:         scheduleCreatedBy(schedule.ID),
					ScheduleID:        &scheduleID,
				})
				if err != nil {
					return fmt.Errorf("insert scheduled broadcast: %w", err)
				}
				if err := q.MarkBroadcastScheduleFired(ctx, sqlcgen.MarkBroadcastScheduleFiredParams{
					NextRunAt:   scheduleTimestamptz(decision.NextRunAt),
					FiredAt:     scheduleTimestamptz(now),
					BroadcastID: &broadcastID,
					ID:          schedule.ID,
				}); err != nil {
					return fmt.Errorf("mark broadcast schedule fired: %w", err)
				}
				outcome.BroadcastID = broadcastID
				outcome.TargetCount = snapshot.RecipientCount
				return nil
			}
		}
		outcome.SkipReason = skip
		if err := q.MarkBroadcastScheduleSkipped(ctx, sqlcgen.MarkBroadcastScheduleSkippedParams{
			NextRunAt: scheduleTimestamptz(decision.NextRunAt),
			SkippedAt: scheduleTimestamptz(now),
			Reason:    string(skip),
			ID:        schedule.ID,
		}); err != nil {
			return fmt.Errorf("mark broadcast schedule skipped: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.BroadcastScheduleOutcome{}, false, err
	}
	return outcome, fired, nil
}

// scheduleCreatedBy 是计划生成的广播在列表“创建者”一栏的显示值。
func scheduleCreatedBy(id int64) string {
	return fmt.Sprintf("schedule:%d", id)
}

func broadcastScheduleFromModel(row sqlcgen.BroadcastSchedule) domain.BroadcastSchedule {
	out := domain.BroadcastSchedule{
		ID:             row.ID,
		Message:        row.Message,
		Timezone:       row.Timezone,
		TimeOfDay:      int(row.TimeOfDay),
		Weekdays:       domain.BroadcastWeekdays(row.Weekdays),
		Enabled:        row.Enabled,
		NextRunAt:      scheduleTime(row.NextRunAt),
		LastRunAt:      scheduleTime(row.LastRunAt),
		RunCount:       row.RunCount,
		LastSkippedAt:  scheduleTime(row.LastSkippedAt),
		LastSkipReason: domain.BroadcastSkipReason(row.LastSkipReason),
		SkippedCount:   row.SkippedCount,
		CreatedBy:      row.CreatedBy,
		UpdatedBy:      row.UpdatedBy,
		CreatedAt:      scheduleTime(row.CreatedAt),
		UpdatedAt:      scheduleTime(row.UpdatedAt),
	}
	if row.LastBroadcastID != nil {
		out.LastBroadcastID = *row.LastBroadcastID
	}
	return out
}

func scheduleTimestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func scheduleTime(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
