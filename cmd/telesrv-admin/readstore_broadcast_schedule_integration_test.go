package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	broadcastapp "telesrv/internal/app/broadcast"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres"
)

// 后台的计划列表与广播列表都是直接读库：计划按真实存储建出并触发一次，
// 列表要带出星期、下次时间和上次广播，广播行要带出来源计划。
func TestReadStoreBroadcastSchedules(t *testing.T) {
	read, pool := verificationReadStore(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%06d", randomBelow(t, 1_000_000))
	user, err := postgres.NewUserStore(pool).Create(ctx, domain.User{AccessHash: 1, Phone: "+178" + suffix, FirstName: "Sched" + suffix})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	broadcasts := postgres.NewBroadcastStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	schedule, err := broadcasts.CreateBroadcastSchedule(ctx, domain.BroadcastSchedule{
		Message: "定时 " + suffix, Timezone: "Asia/Shanghai", TimeOfDay: 14 * 60,
		Weekdays: domain.BroadcastWeekdaysOf(time.Monday, time.Wednesday, time.Friday), Enabled: true, NextRunAt: now.Add(-time.Minute),
	}, "ops")
	if err != nil {
		t.Fatalf("CreateBroadcastSchedule: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM broadcasts WHERE schedule_id = $1`, schedule.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM broadcast_schedules WHERE id = $1`, schedule.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	outcome, ok, err := broadcasts.FireDueBroadcastSchedule(ctx, now, func(due store.DueBroadcastSchedule) store.BroadcastScheduleFiring {
		return broadcastapp.PlanScheduleFiring(due, now)
	})
	if err != nil || !ok || outcome.BroadcastID == 0 {
		t.Fatalf("fire = %+v ok=%v err=%v", outcome, ok, err)
	}

	rows, err := read.ListBroadcastSchedules(ctx)
	if err != nil {
		t.Fatalf("ListBroadcastSchedules: %v", err)
	}
	var row *BroadcastScheduleRow
	for i := range rows {
		if rows[i].ID == schedule.ID {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("schedule %d missing from %+v", schedule.ID, rows)
	}
	if fmt.Sprint(row.Weekdays) != "[1 3 5]" || row.NextRunAt == nil || !row.NextRunAt.After(now) || row.LastRunAt == nil ||
		row.LastBroadcastID != outcome.BroadcastID || row.RunCount != 1 || row.CreatedBy != "ops" {
		t.Fatalf("schedule row = %+v", *row)
	}

	list, _, err := read.ListBroadcasts(ctx, 0, 100)
	if err != nil {
		t.Fatalf("ListBroadcasts: %v", err)
	}
	for _, b := range list {
		if b.ID == outcome.BroadcastID {
			if b.ScheduleID != schedule.ID {
				t.Fatalf("broadcast row schedule = %d, want %d", b.ScheduleID, schedule.ID)
			}
			return
		}
	}
	t.Fatalf("scheduled broadcast %d missing from list", outcome.BroadcastID)
}
