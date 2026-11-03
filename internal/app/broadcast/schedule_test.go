package broadcast

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestNextScheduleRun(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	newYork := mustLocation(t, "America/New_York")
	monWedFri := domain.BroadcastWeekdaysOf(time.Monday, time.Wednesday, time.Friday)
	const at1400 = 14 * 60
	// 2026-11-02 是周一。
	tests := []struct {
		name     string
		after    time.Time
		loc      *time.Location
		minute   int
		weekdays domain.BroadcastWeekdays
		want     time.Time
	}{
		{name: "every day before the time fires today", after: time.Date(2026, 11, 2, 9, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: domain.BroadcastEveryDay,
			want: time.Date(2026, 11, 2, 14, 0, 0, 0, shanghai)},
		{name: "every day after the time fires tomorrow", after: time.Date(2026, 11, 2, 15, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: domain.BroadcastEveryDay,
			want: time.Date(2026, 11, 3, 14, 0, 0, 0, shanghai)},
		{name: "exactly at the time is not again", after: time.Date(2026, 11, 2, 14, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: domain.BroadcastEveryDay,
			want: time.Date(2026, 11, 3, 14, 0, 0, 0, shanghai)},
		{name: "monday after the time goes to wednesday", after: time.Date(2026, 11, 2, 15, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: monWedFri,
			want: time.Date(2026, 11, 4, 14, 0, 0, 0, shanghai)},
		{name: "friday after the time wraps to monday", after: time.Date(2026, 11, 6, 15, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: monWedFri,
			want: time.Date(2026, 11, 9, 14, 0, 0, 0, shanghai)},
		{name: "single weekday after the time waits a full week", after: time.Date(2026, 11, 2, 15, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: domain.BroadcastWeekdaysOf(time.Monday),
			want: time.Date(2026, 11, 9, 14, 0, 0, 0, shanghai)},
		{name: "local weekday is used, not UTC", after: time.Date(2026, 11, 2, 17, 0, 0, 0, time.UTC), loc: shanghai, minute: 9 * 60, weekdays: domain.BroadcastWeekdaysOf(time.Tuesday),
			// UTC 周一 17:00 已是上海周二 01:00，当天 09:00 还没到。
			want: time.Date(2026, 11, 3, 9, 0, 0, 0, shanghai)},
		{name: "wall clock holds across a DST change", after: time.Date(2026, 3, 7, 15, 0, 0, 0, newYork), loc: newYork, minute: at1400, weekdays: domain.BroadcastEveryDay,
			want: time.Date(2026, 3, 8, 14, 0, 0, 0, newYork)},
		{name: "empty weekdays never fires", after: time.Date(2026, 11, 2, 9, 0, 0, 0, shanghai), loc: shanghai, minute: at1400, weekdays: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextScheduleRun(tt.after, tt.loc, tt.minute, tt.weekdays)
			if !got.Equal(tt.want) {
				t.Fatalf("NextScheduleRun = %v, want %v", got, tt.want)
			}
		})
	}
	// 夏令时当天仍是当地 14:00，UTC 偏移从 -5 变 -4。
	dst := NextScheduleRun(time.Date(2026, 3, 7, 15, 0, 0, 0, newYork), newYork, at1400, domain.BroadcastEveryDay)
	if _, offset := dst.Zone(); offset != -4*3600 {
		t.Fatalf("DST-day run offset = %d, want -4h", offset)
	}
}

func TestPreviewScheduleValidatesAndListsUpcomingRuns(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	service := NewService(&fakeBroadcastStore{}, nil, nil)
	service.now = func() time.Time { return time.Date(2026, 11, 2, 15, 0, 0, 0, shanghai) }

	preview, err := service.PreviewSchedule(ScheduleInput{
		Message:   "  每周公告  ",
		TimeOfDay: 14 * 60,
		Weekdays:  domain.BroadcastWeekdaysOf(time.Monday, time.Wednesday, time.Friday),
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("PreviewSchedule: %v", err)
	}
	if preview.Schedule.Message != "每周公告" || preview.Schedule.Timezone != domain.DefaultBroadcastTimezone {
		t.Fatalf("normalized schedule = %+v", preview.Schedule)
	}
	want := []time.Time{
		time.Date(2026, 11, 4, 14, 0, 0, 0, shanghai),
		time.Date(2026, 11, 6, 14, 0, 0, 0, shanghai),
		time.Date(2026, 11, 9, 14, 0, 0, 0, shanghai),
	}
	if len(preview.Upcoming) != len(want) {
		t.Fatalf("upcoming = %v, want %v", preview.Upcoming, want)
	}
	for i := range want {
		if !preview.Upcoming[i].Equal(want[i]) {
			t.Fatalf("upcoming[%d] = %v, want %v", i, preview.Upcoming[i], want[i])
		}
	}
	if !preview.Schedule.NextRunAt.Equal(want[0]) {
		t.Fatalf("next run = %v, want %v", preview.Schedule.NextRunAt, want[0])
	}

	disabled, err := service.PreviewSchedule(ScheduleInput{Message: "x", TimeOfDay: 0, Weekdays: domain.BroadcastEveryDay})
	if err != nil {
		t.Fatalf("PreviewSchedule disabled: %v", err)
	}
	if !disabled.Schedule.NextRunAt.IsZero() || len(disabled.Upcoming) == 0 {
		t.Fatalf("disabled schedule next=%v upcoming=%v, want no next run but a preview", disabled.Schedule.NextRunAt, disabled.Upcoming)
	}

	invalid := []struct {
		name  string
		input ScheduleInput
		want  error
	}{
		{name: "empty message", input: ScheduleInput{Message: " ", Weekdays: domain.BroadcastEveryDay}, want: domain.ErrBroadcastMessageEmpty},
		{name: "too long", input: ScheduleInput{Message: strings.Repeat("x", domain.MaxBroadcastMessageBytes+1), Weekdays: domain.BroadcastEveryDay}, want: domain.ErrBroadcastMessageTooLong},
		{name: "unknown zone", input: ScheduleInput{Message: "x", Timezone: "Mars/Base", Weekdays: domain.BroadcastEveryDay}, want: domain.ErrBroadcastScheduleInvalidZone},
		{name: "machine-local zone", input: ScheduleInput{Message: "x", Timezone: "Local", Weekdays: domain.BroadcastEveryDay}, want: domain.ErrBroadcastScheduleInvalidZone},
		{name: "minute past midnight", input: ScheduleInput{Message: "x", TimeOfDay: 24 * 60, Weekdays: domain.BroadcastEveryDay}, want: domain.ErrBroadcastScheduleInvalidTime},
		{name: "negative minute", input: ScheduleInput{Message: "x", TimeOfDay: -1, Weekdays: domain.BroadcastEveryDay}, want: domain.ErrBroadcastScheduleInvalidTime},
		{name: "no weekdays", input: ScheduleInput{Message: "x"}, want: domain.ErrBroadcastScheduleInvalidDays},
		{name: "weekday bit out of range", input: ScheduleInput{Message: "x", Weekdays: 0x80}, want: domain.ErrBroadcastScheduleInvalidDays},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := service.PreviewSchedule(tt.input); !errors.Is(err, tt.want) {
				t.Fatalf("PreviewSchedule error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPlanScheduleFiring(t *testing.T) {
	shanghai := mustLocation(t, "Asia/Shanghai")
	due := time.Date(2026, 11, 2, 14, 0, 0, 0, shanghai)
	tomorrow := time.Date(2026, 11, 3, 14, 0, 0, 0, shanghai)
	schedule := domain.BroadcastSchedule{
		ID: 7, Message: "见 @matrixG", Timezone: "Asia/Shanghai", TimeOfDay: 14 * 60,
		Weekdays: domain.BroadcastEveryDay, Enabled: true, NextRunAt: due,
	}
	tests := []struct {
		name     string
		due      store.DueBroadcastSchedule
		now      time.Time
		wantSkip domain.BroadcastSkipReason
		wantNext time.Time
	}{
		{name: "on time sends", due: store.DueBroadcastSchedule{Schedule: schedule}, now: due.Add(3 * time.Second), wantNext: tomorrow},
		{name: "late within grace still sends", due: store.DueBroadcastSchedule{Schedule: schedule}, now: due.Add(domain.BroadcastScheduleMissedGrace), wantNext: tomorrow},
		{name: "past grace is missed", due: store.DueBroadcastSchedule{Schedule: schedule}, now: due.Add(domain.BroadcastScheduleMissedGrace + time.Second), wantSkip: domain.BroadcastSkipMissed, wantNext: tomorrow},
		{name: "down for days skips to the next future run only", due: store.DueBroadcastSchedule{Schedule: schedule}, now: due.Add(72*time.Hour + time.Hour + time.Minute),
			wantSkip: domain.BroadcastSkipMissed, wantNext: time.Date(2026, 11, 6, 14, 0, 0, 0, shanghai)},
		{name: "previous broadcast still delivering", due: store.DueBroadcastSchedule{Schedule: schedule, PreviousRunning: true}, now: due.Add(time.Second), wantSkip: domain.BroadcastSkipPreviousRunning, wantNext: tomorrow},
		{name: "unknown zone disables", due: store.DueBroadcastSchedule{Schedule: func() domain.BroadcastSchedule { s := schedule; s.Timezone = "Mars/Base"; return s }()}, now: due, wantSkip: domain.BroadcastSkipInvalidTimezone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanScheduleFiring(tt.due, tt.now)
			if got.SkipReason != tt.wantSkip {
				t.Fatalf("skip = %q, want %q", got.SkipReason, tt.wantSkip)
			}
			if !got.NextRunAt.Equal(tt.wantNext) {
				t.Fatalf("next = %v, want %v", got.NextRunAt, tt.wantNext)
			}
			if tt.wantSkip == "" && len(got.Entities) != 1 {
				t.Fatalf("send entities = %+v, want the detected mention", got.Entities)
			}
		})
	}
}

func TestRunCycleFiresDueSchedulesBeforeDelivery(t *testing.T) {
	st := &fakeBroadcastStore{}
	schedules := &fakeScheduleStore{due: []store.DueBroadcastSchedule{
		{Schedule: domain.BroadcastSchedule{ID: 1, Message: "a", Timezone: "UTC", Weekdays: domain.BroadcastEveryDay, Enabled: true}},
		{Schedule: domain.BroadcastSchedule{ID: 2, Message: "b", Timezone: "UTC", Weekdays: domain.BroadcastEveryDay, Enabled: true}, PreviousRunning: true},
	}}
	now := time.Date(2026, 11, 2, 0, 0, 30, 0, time.UTC)
	service := NewService(st, &fakeBroadcastDelivery{}, nil).WithSchedules(schedules)
	service.now = func() time.Time { return now }
	for i := range schedules.due {
		schedules.due[i].Schedule.NextRunAt = now.Add(-30 * time.Second)
	}

	result, err := service.RunCycle(context.Background(), "lease", 10, 10, time.Minute)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if result.ScheduledCreated != 1 || result.ScheduledSkipped != 1 {
		t.Fatalf("scheduled created/skipped = %d/%d, want 1/1", result.ScheduledCreated, result.ScheduledSkipped)
	}
	if schedules.calls != 3 {
		t.Fatalf("fire calls = %d, want 3 (two due, then empty)", schedules.calls)
	}
}

func TestRunCycleDeliversWhenSchedulesFail(t *testing.T) {
	st := &fakeBroadcastStore{claims: []store.BroadcastRecipientClaim{{RecipientID: 1, BroadcastID: 1, UserID: 101, LeaseToken: "lease"}}}
	delivery := &fakeBroadcastDelivery{}
	service := NewService(st, delivery, nil).WithSchedules(&fakeScheduleStore{err: errors.New("schedule table locked")})

	result, err := service.RunCycle(context.Background(), "lease", 10, 10, time.Minute)
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if result.Sent != 1 || delivery.calls != 1 {
		t.Fatalf("sent=%d delivery calls=%d, want manual broadcasts still delivered", result.Sent, delivery.calls)
	}
}

// fakeScheduleStore 依次交出 due 里的到期计划，模拟每次只锁一条。
type fakeScheduleStore struct {
	due   []store.DueBroadcastSchedule
	err   error
	calls int
}

func (s *fakeScheduleStore) FireDueBroadcastSchedule(_ context.Context, _ time.Time, plan store.BroadcastSchedulePlanner) (store.BroadcastScheduleOutcome, bool, error) {
	s.calls++
	if s.err != nil {
		return store.BroadcastScheduleOutcome{}, false, s.err
	}
	if len(s.due) == 0 {
		return store.BroadcastScheduleOutcome{}, false, nil
	}
	due := s.due[0]
	s.due = s.due[1:]
	decision := plan(due)
	outcome := store.BroadcastScheduleOutcome{ScheduleID: due.Schedule.ID, SkipReason: decision.SkipReason, NextRunAt: decision.NextRunAt}
	if decision.SkipReason == "" {
		outcome.BroadcastID = due.Schedule.ID * 100
	}
	return outcome, true, nil
}

func (s *fakeScheduleStore) CreateBroadcastSchedule(_ context.Context, schedule domain.BroadcastSchedule, _ string) (domain.BroadcastSchedule, error) {
	return schedule, nil
}

func (s *fakeScheduleStore) UpdateBroadcastSchedule(_ context.Context, schedule domain.BroadcastSchedule, _ string) (domain.BroadcastSchedule, error) {
	return schedule, nil
}

func (s *fakeScheduleStore) DeleteBroadcastSchedule(context.Context, int64) error { return nil }

func (s *fakeScheduleStore) GetBroadcastSchedule(context.Context, int64) (domain.BroadcastSchedule, error) {
	return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
}

func (s *fakeScheduleStore) ListBroadcastSchedules(context.Context, int) ([]domain.BroadcastSchedule, error) {
	return nil, nil
}
