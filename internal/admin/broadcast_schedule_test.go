package admin

import (
	"context"
	"reflect"
	"testing"
	"time"

	broadcastapp "telesrv/internal/app/broadcast"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 自动广播计划命令的命令层测试：业务服务用真的 app/broadcast.Service，存储用内存替身。

type memoryScheduleStore struct {
	nextID int64
	items  map[int64]domain.BroadcastSchedule
	actors map[int64]string
}

func newMemoryScheduleStore() *memoryScheduleStore {
	return &memoryScheduleStore{items: map[int64]domain.BroadcastSchedule{}, actors: map[int64]string{}}
}

func (m *memoryScheduleStore) CreateBroadcastSchedule(_ context.Context, schedule domain.BroadcastSchedule, actor string) (domain.BroadcastSchedule, error) {
	m.nextID++
	schedule.ID = m.nextID
	m.items[schedule.ID] = schedule
	m.actors[schedule.ID] = actor
	return schedule, nil
}

func (m *memoryScheduleStore) UpdateBroadcastSchedule(_ context.Context, schedule domain.BroadcastSchedule, actor string) (domain.BroadcastSchedule, error) {
	if _, ok := m.items[schedule.ID]; !ok {
		return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
	}
	m.items[schedule.ID] = schedule
	m.actors[schedule.ID] = actor
	return schedule, nil
}

func (m *memoryScheduleStore) DeleteBroadcastSchedule(_ context.Context, id int64) error {
	if _, ok := m.items[id]; !ok {
		return domain.ErrBroadcastScheduleNotFound
	}
	delete(m.items, id)
	return nil
}

func (m *memoryScheduleStore) GetBroadcastSchedule(_ context.Context, id int64) (domain.BroadcastSchedule, error) {
	item, ok := m.items[id]
	if !ok {
		return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
	}
	return item, nil
}

func (m *memoryScheduleStore) ListBroadcastSchedules(context.Context, int) ([]domain.BroadcastSchedule, error) {
	return nil, nil
}

func (m *memoryScheduleStore) FireDueBroadcastSchedule(context.Context, time.Time, store.BroadcastSchedulePlanner) (store.BroadcastScheduleOutcome, bool, error) {
	return store.BroadcastScheduleOutcome{}, false, nil
}

func newScheduleFixture() (*Service, *memoryScheduleStore, *memoryCommandRepo) {
	schedules := newMemoryScheduleStore()
	repo := newMemoryCommandRepo()
	svc := NewService(Dependencies{
		Commands:           repo,
		BroadcastSchedules: broadcastapp.NewService(nil, nil, nil).WithSchedules(schedules),
		Now:                fixedNow,
	})
	return svc, schedules, repo
}

func scheduleMeta(id string, dryRun bool) CommandMeta {
	return CommandMeta{CommandID: id, Actor: "ops", Reason: "weekly notice", DryRun: dryRun}
}

func TestCreateBroadcastScheduleDryRunThenConfirm(t *testing.T) {
	svc, schedules, repo := newScheduleFixture()
	ctx := context.Background()
	fields := BroadcastScheduleFields{Message: " 周一三五公告 ", TimeOfDay: 14 * 60, Weekdays: []int{5, 1, 3, 1}, Enabled: true}

	dry, err := svc.CreateBroadcastSchedule(ctx, CreateBroadcastScheduleRequest{CommandMeta: scheduleMeta("dry-sched", true), BroadcastScheduleFields: fields})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !dry.DryRun || dry.Details["timezone"] != domain.DefaultBroadcastTimezone || dry.Details["time_of_day"] != "14:00" ||
		!reflect.DeepEqual(dry.Details["weekdays"], []int{1, 3, 5}) || len(dry.Details["upcoming"].([]string)) != 3 {
		t.Fatalf("dry-run result = %+v", dry)
	}
	if len(schedules.items) != 0 {
		t.Fatalf("dry-run stored %d schedules", len(schedules.items))
	}

	res, err := svc.CreateBroadcastSchedule(ctx, CreateBroadcastScheduleRequest{CommandMeta: scheduleMeta("sched-1", false), BroadcastScheduleFields: fields})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionCreateBroadcastSchedule || res.Details["schedule_id"] != "1" {
		t.Fatalf("confirm result = %+v", res)
	}
	stored := schedules.items[1]
	if stored.Message != "周一三五公告" || stored.Weekdays != domain.BroadcastWeekdaysOf(1, 3, 5) || !stored.Enabled || stored.NextRunAt.IsZero() ||
		schedules.actors[1] != "ops" {
		t.Fatalf("stored schedule = %+v actor=%q", stored, schedules.actors[1])
	}
	if rec := repo.items["sched-1"]; rec.Action != ActionCreateBroadcastSchedule || rec.Reason != "weekly notice" {
		t.Fatalf("audit record = %+v", rec)
	}
}

func TestBroadcastScheduleCommandErrorsCarryCodes(t *testing.T) {
	svc, _, _ := newScheduleFixture()
	ctx := context.Background()

	res, err := svc.CreateBroadcastSchedule(ctx, CreateBroadcastScheduleRequest{
		CommandMeta:             scheduleMeta("bad-day", true),
		BroadcastScheduleFields: BroadcastScheduleFields{Message: "x", Weekdays: []int{7}},
	})
	if err == nil || res.Code != CodeBroadcastScheduleInvalid {
		t.Fatalf("weekday 7: code=%q err=%v, want %s", res.Code, err, CodeBroadcastScheduleInvalid)
	}
	res, err = svc.CreateBroadcastSchedule(ctx, CreateBroadcastScheduleRequest{
		CommandMeta:             scheduleMeta("bad-zone", true),
		BroadcastScheduleFields: BroadcastScheduleFields{Message: "x", Timezone: "Mars/Base", Weekdays: []int{1}},
	})
	if err == nil || res.Code != CodeBroadcastScheduleInvalid {
		t.Fatalf("bad zone: code=%q err=%v, want %s", res.Code, err, CodeBroadcastScheduleInvalid)
	}
	res, err = svc.UpdateBroadcastSchedule(ctx, UpdateBroadcastScheduleRequest{
		CommandMeta:             scheduleMeta("missing", true),
		ScheduleID:              99,
		BroadcastScheduleFields: BroadcastScheduleFields{Message: "x", Weekdays: []int{1}},
	})
	if err == nil || res.Code != CodeBroadcastScheduleNotFound {
		t.Fatalf("update missing: code=%q err=%v, want %s", res.Code, err, CodeBroadcastScheduleNotFound)
	}
}

func TestUpdateAndDeleteBroadcastSchedule(t *testing.T) {
	svc, schedules, _ := newScheduleFixture()
	ctx := context.Background()
	if _, err := svc.CreateBroadcastSchedule(ctx, CreateBroadcastScheduleRequest{
		CommandMeta:             scheduleMeta("create", false),
		BroadcastScheduleFields: BroadcastScheduleFields{Message: "每日", TimeOfDay: 600, Weekdays: []int{0, 1, 2, 3, 4, 5, 6}, Enabled: true},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 停用：整体替换，没有下次触发时间。
	if _, err := svc.UpdateBroadcastSchedule(ctx, UpdateBroadcastScheduleRequest{
		CommandMeta:             scheduleMeta("disable", false),
		ScheduleID:              1,
		BroadcastScheduleFields: BroadcastScheduleFields{Message: "每日", TimeOfDay: 600, Weekdays: []int{0, 1, 2, 3, 4, 5, 6}, Enabled: false},
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := schedules.items[1]; got.Enabled || !got.NextRunAt.IsZero() || got.Weekdays != domain.BroadcastEveryDay {
		t.Fatalf("disabled schedule = %+v", got)
	}

	dry, err := svc.DeleteBroadcastSchedule(ctx, DeleteBroadcastScheduleRequest{CommandMeta: scheduleMeta("del-dry", true), ScheduleID: 1})
	if err != nil || dry.Details["message_preview"] != "每日" || len(schedules.items) != 1 {
		t.Fatalf("delete dry-run = %+v err=%v items=%d", dry, err, len(schedules.items))
	}
	if _, err := svc.DeleteBroadcastSchedule(ctx, DeleteBroadcastScheduleRequest{CommandMeta: scheduleMeta("del", false), ScheduleID: 1}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(schedules.items) != 0 {
		t.Fatalf("schedules after delete = %+v", schedules.items)
	}
}
