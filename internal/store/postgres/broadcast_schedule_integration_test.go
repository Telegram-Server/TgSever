package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	broadcastapp "telesrv/internal/app/broadcast"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func dueTestSchedule(t *testing.T, ctx context.Context, st *BroadcastStore, timezone string, nextRunAt time.Time) domain.BroadcastSchedule {
	t.Helper()
	schedule, err := st.CreateBroadcastSchedule(ctx, domain.BroadcastSchedule{
		Message:   "每日公告 @matrixG",
		Timezone:  timezone,
		TimeOfDay: nextRunAt.UTC().Hour()*60 + nextRunAt.UTC().Minute(),
		Weekdays:  domain.BroadcastEveryDay,
		Enabled:   true,
		NextRunAt: nextRunAt,
	}, "integration-test")
	if err != nil {
		t.Fatalf("CreateBroadcastSchedule: %v", err)
	}
	return schedule
}

func fireDue(t *testing.T, ctx context.Context, st *BroadcastStore, now time.Time) (store.BroadcastScheduleOutcome, bool) {
	t.Helper()
	outcome, ok, err := st.FireDueBroadcastSchedule(ctx, now, func(due store.DueBroadcastSchedule) store.BroadcastScheduleFiring {
		return broadcastapp.PlanScheduleFiring(due, now)
	})
	if err != nil {
		t.Fatalf("FireDueBroadcastSchedule: %v", err)
	}
	return outcome, ok
}

func TestBroadcastScheduleFiresOnceAndAdvances(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	createLoginCodeDeliveryTestUser(t, ctx, pool, "ScheduleFire")
	st := NewBroadcastStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	schedule := dueTestSchedule(t, ctx, st, "UTC", now.Add(-time.Minute))

	outcome, ok := fireDue(t, ctx, st, now)
	if !ok || outcome.SkipReason != "" || outcome.BroadcastID == 0 || outcome.ScheduleID != schedule.ID {
		t.Fatalf("first fire = %+v ok=%v, want a created broadcast", outcome, ok)
	}
	var (
		scheduleID      int64
		targetMode      string
		targetCount     int64
		createdBy       string
		enumerationDone bool
		entities        string
	)
	if err := pool.QueryRow(ctx, `
SELECT schedule_id, target_mode, target_count, created_by, enumeration_done, entities::text
FROM broadcasts WHERE id = $1`, outcome.BroadcastID).Scan(&scheduleID, &targetMode, &targetCount, &createdBy, &enumerationDone, &entities); err != nil {
		t.Fatalf("read scheduled broadcast: %v", err)
	}
	if scheduleID != schedule.ID || targetMode != "all" || targetCount < 1 || enumerationDone || createdBy != scheduleCreatedBy(schedule.ID) {
		t.Fatalf("broadcast schedule=%d mode=%s count=%d by=%q done=%v", scheduleID, targetMode, targetCount, createdBy, enumerationDone)
	}
	if entities == "[]" || entities == "" {
		t.Fatalf("scheduled broadcast entities = %s, want the detected mention", entities)
	}

	got, err := st.GetBroadcastSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("GetBroadcastSchedule: %v", err)
	}
	if !got.NextRunAt.After(now) || got.RunCount != 1 || got.LastBroadcastID != outcome.BroadcastID || !got.LastRunAt.Equal(now) || !got.Enabled {
		t.Fatalf("schedule after fire = %+v", got)
	}

	// 下一次还没到：同一时刻再取一次什么都不做。
	if outcome, ok := fireDue(t, ctx, st, now); ok {
		t.Fatalf("second fire at the same instant = %+v, want nothing due", outcome)
	}
}

func TestBroadcastScheduleSkipsWhilePreviousBroadcastRuns(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	createLoginCodeDeliveryTestUser(t, ctx, pool, "ScheduleSkip")
	st := NewBroadcastStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	schedule := dueTestSchedule(t, ctx, st, "UTC", now.Add(-time.Minute))
	first, ok := fireDue(t, ctx, st, now)
	if !ok || first.BroadcastID == 0 {
		t.Fatalf("first fire = %+v ok=%v", first, ok)
	}

	// 第一条广播还没枚举收件人，计划又到点了。
	rewindSchedule(t, ctx, pool, schedule.ID, now.Add(-time.Second))
	second, ok := fireDue(t, ctx, st, now)
	if !ok || second.SkipReason != domain.BroadcastSkipPreviousRunning || second.BroadcastID != 0 {
		t.Fatalf("second fire = %+v ok=%v, want skipped previous_running", second, ok)
	}
	got, err := st.GetBroadcastSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("GetBroadcastSchedule: %v", err)
	}
	if got.SkippedCount != 1 || got.LastSkipReason != domain.BroadcastSkipPreviousRunning || !got.LastSkippedAt.Equal(now) ||
		got.RunCount != 1 || !got.NextRunAt.After(now) {
		t.Fatalf("schedule after skip = %+v", got)
	}
	var broadcasts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM broadcasts WHERE schedule_id = $1`, schedule.ID).Scan(&broadcasts); err != nil {
		t.Fatalf("count scheduled broadcasts: %v", err)
	}
	if broadcasts != 1 {
		t.Fatalf("scheduled broadcasts = %d, want 1", broadcasts)
	}

	// 上一条投递完之后照常发送。
	if _, err := pool.Exec(ctx, `UPDATE broadcasts SET enumeration_done = true, materialized_count = 0 WHERE id = $1`, first.BroadcastID); err != nil {
		t.Fatalf("finish first broadcast: %v", err)
	}
	rewindSchedule(t, ctx, pool, schedule.ID, now.Add(-time.Second))
	if third, ok := fireDue(t, ctx, st, now); !ok || third.SkipReason != "" || third.BroadcastID == 0 {
		t.Fatalf("third fire = %+v ok=%v, want a created broadcast", third, ok)
	}
}

func TestBroadcastScheduleConcurrentWorkersFireOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	createLoginCodeDeliveryTestUser(t, ctx, pool, "ScheduleRace")
	st := NewBroadcastStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	schedule := dueTestSchedule(t, ctx, st, "UTC", now.Add(-time.Minute))

	const workers = 6
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := st.FireDueBroadcastSchedule(ctx, now, func(due store.DueBroadcastSchedule) store.BroadcastScheduleFiring {
				return broadcastapp.PlanScheduleFiring(due, now)
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent fire: %v", err)
		}
	}
	var broadcasts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM broadcasts WHERE schedule_id = $1`, schedule.ID).Scan(&broadcasts); err != nil {
		t.Fatalf("count scheduled broadcasts: %v", err)
	}
	if broadcasts != 1 {
		t.Fatalf("concurrent workers created %d broadcasts, want exactly 1", broadcasts)
	}
}

func TestBroadcastScheduleMissedAndInvalidZone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	createLoginCodeDeliveryTestUser(t, ctx, pool, "ScheduleMissed")
	st := NewBroadcastStore(pool)
	now := time.Now().UTC().Truncate(time.Second)

	missed := dueTestSchedule(t, ctx, st, "UTC", now.Add(-domain.BroadcastScheduleMissedGrace-time.Minute))
	outcome, ok := fireDue(t, ctx, st, now)
	if !ok || outcome.ScheduleID != missed.ID || outcome.SkipReason != domain.BroadcastSkipMissed {
		t.Fatalf("missed fire = %+v ok=%v", outcome, ok)
	}

	// 时区在本机时区库里消失（例如换了精简镜像）：本次跳过并停用，不会每轮都重试。
	broken := dueTestSchedule(t, ctx, st, "Mars/Base", now.Add(-time.Minute))
	outcome, ok = fireDue(t, ctx, st, now)
	if !ok || outcome.ScheduleID != broken.ID || outcome.SkipReason != domain.BroadcastSkipInvalidTimezone {
		t.Fatalf("broken zone fire = %+v ok=%v", outcome, ok)
	}
	got, err := st.GetBroadcastSchedule(ctx, broken.ID)
	if err != nil {
		t.Fatalf("GetBroadcastSchedule: %v", err)
	}
	if got.Enabled || !got.NextRunAt.IsZero() || got.LastSkipReason != domain.BroadcastSkipInvalidTimezone {
		t.Fatalf("broken schedule = %+v, want disabled", got)
	}
	var broadcasts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM broadcasts WHERE schedule_id IS NOT NULL`).Scan(&broadcasts); err != nil {
		t.Fatalf("count scheduled broadcasts: %v", err)
	}
	if broadcasts != 0 {
		t.Fatalf("skipped schedules created %d broadcasts", broadcasts)
	}
}

func TestBroadcastScheduleCRUD(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	createLoginCodeDeliveryTestUser(t, ctx, pool, "ScheduleCRUD")
	st := NewBroadcastStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	schedule := dueTestSchedule(t, ctx, st, "UTC", now.Add(-time.Minute))
	fired, ok := fireDue(t, ctx, st, now)
	if !ok || fired.BroadcastID == 0 {
		t.Fatalf("fire = %+v ok=%v", fired, ok)
	}

	schedule.Message = "改过的内容"
	schedule.Timezone = "Asia/Shanghai"
	schedule.Weekdays = domain.BroadcastWeekdaysOf(time.Monday, time.Friday)
	schedule.Enabled = false
	schedule.NextRunAt = time.Time{}
	updated, err := st.UpdateBroadcastSchedule(ctx, schedule, "editor")
	if err != nil {
		t.Fatalf("UpdateBroadcastSchedule: %v", err)
	}
	if updated.Message != "改过的内容" || updated.Enabled || !updated.NextRunAt.IsZero() || updated.UpdatedBy != "editor" ||
		updated.CreatedBy != "integration-test" || updated.RunCount != 1 || updated.Weekdays != schedule.Weekdays {
		t.Fatalf("updated schedule = %+v", updated)
	}
	list, err := st.ListBroadcastSchedules(ctx, 10)
	if err != nil || len(list) != 1 || list[0].ID != schedule.ID {
		t.Fatalf("ListBroadcastSchedules = %+v err=%v", list, err)
	}

	if err := st.DeleteBroadcastSchedule(ctx, schedule.ID); err != nil {
		t.Fatalf("DeleteBroadcastSchedule: %v", err)
	}
	if err := st.DeleteBroadcastSchedule(ctx, schedule.ID); !errors.Is(err, domain.ErrBroadcastScheduleNotFound) {
		t.Fatalf("second delete error = %v, want not found", err)
	}
	if _, err := st.UpdateBroadcastSchedule(ctx, schedule, "editor"); !errors.Is(err, domain.ErrBroadcastScheduleNotFound) {
		t.Fatalf("update deleted schedule error = %v, want not found", err)
	}
	// 删除计划不删它发过的广播，只断开关联。
	var scheduleID *int64
	if err := pool.QueryRow(ctx, `SELECT schedule_id FROM broadcasts WHERE id = $1`, fired.BroadcastID).Scan(&scheduleID); err != nil {
		t.Fatalf("read broadcast after schedule delete: %v", err)
	}
	if scheduleID != nil {
		t.Fatalf("broadcast schedule_id = %d after delete, want NULL", *scheduleID)
	}
}

func rewindSchedule(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE broadcast_schedules SET next_run_at = $2 WHERE id = $1`, id, at); err != nil {
		t.Fatalf("rewind schedule: %v", err)
	}
}
