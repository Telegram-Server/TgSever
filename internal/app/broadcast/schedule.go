package broadcast

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ScheduleInput 是后台新建或修改自动广播计划时提交的内容。自动广播一律发给全部账号。
type ScheduleInput struct {
	Message   string
	Timezone  string
	TimeOfDay int
	Weekdays  domain.BroadcastWeekdays
	Enabled   bool
}

// SchedulePreview 是校验通过的计划和它接下来的几次触发时间（试运行展示用）。
type SchedulePreview struct {
	Schedule domain.BroadcastSchedule
	Upcoming []time.Time
}

const (
	scheduleUpcomingPreview = 3
	// scheduleFireBatch 是每轮 worker 最多处理的到期计划数，剩下的下一轮再取。
	scheduleFireBatch = 20
)

// WithSchedules 接上自动广播计划存储；不接时 worker 只投递手动广播。
func (s *Service) WithSchedules(st store.BroadcastScheduleStore) *Service {
	if s != nil {
		s.schedules = st
	}
	return s
}

// NextScheduleRun 返回严格晚于 after 的下一次触发时刻。时刻按 loc 的当地时间解释，
// 落在夏令时跳过的那一小时里时由 time.Date 顺延。weekdays 为空时返回零值。
func NextScheduleRun(after time.Time, loc *time.Location, timeOfDay int, weekdays domain.BroadcastWeekdays) time.Time {
	if loc == nil || !weekdays.Valid() || timeOfDay < 0 || timeOfDay >= 24*60 {
		return time.Time{}
	}
	local := after.In(loc)
	// 8 天足够：今天的时刻已过时，最远是下周的今天。
	for i := 0; i <= 7; i++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+i, 0, 0, 0, 0, loc)
		if !weekdays.Has(day.Weekday()) {
			continue
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), timeOfDay/60, timeOfDay%60, 0, 0, loc)
		if candidate.After(after) {
			return candidate
		}
	}
	return time.Time{}
}

func normalizeSchedule(input ScheduleInput) (domain.BroadcastSchedule, *time.Location, error) {
	message, _, err := normalizeRequest(input.Message, domain.BroadcastTargetAll, nil)
	if err != nil {
		return domain.BroadcastSchedule{}, nil, err
	}
	zone := strings.TrimSpace(input.Timezone)
	if zone == "" {
		zone = domain.DefaultBroadcastTimezone
	}
	// time.LoadLocation 接受 "Local" 与空串，那两个依赖运行机器的设置，不能存进计划。
	if zone == "Local" || len(zone) > 64 {
		return domain.BroadcastSchedule{}, nil, domain.ErrBroadcastScheduleInvalidZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return domain.BroadcastSchedule{}, nil, domain.ErrBroadcastScheduleInvalidZone
	}
	if input.TimeOfDay < 0 || input.TimeOfDay >= 24*60 {
		return domain.BroadcastSchedule{}, nil, domain.ErrBroadcastScheduleInvalidTime
	}
	if !input.Weekdays.Valid() {
		return domain.BroadcastSchedule{}, nil, domain.ErrBroadcastScheduleInvalidDays
	}
	return domain.BroadcastSchedule{
		Message:   message,
		Timezone:  zone,
		TimeOfDay: input.TimeOfDay,
		Weekdays:  input.Weekdays,
		Enabled:   input.Enabled,
	}, loc, nil
}

// PreviewSchedule 校验计划并算出下次触发时间与之后几次，不写库。停用的计划没有下次时间，
// 但仍给出启用后会触发的时刻，方便运营核对。
func (s *Service) PreviewSchedule(input ScheduleInput) (SchedulePreview, error) {
	schedule, loc, err := normalizeSchedule(input)
	if err != nil {
		return SchedulePreview{}, err
	}
	now := s.clock()
	upcoming := make([]time.Time, 0, scheduleUpcomingPreview)
	cursor := now
	for len(upcoming) < scheduleUpcomingPreview {
		next := NextScheduleRun(cursor, loc, schedule.TimeOfDay, schedule.Weekdays)
		if next.IsZero() {
			break
		}
		upcoming = append(upcoming, next)
		cursor = next
	}
	if schedule.Enabled && len(upcoming) > 0 {
		schedule.NextRunAt = upcoming[0]
	}
	return SchedulePreview{Schedule: schedule, Upcoming: upcoming}, nil
}

func (s *Service) scheduleStore() (store.BroadcastScheduleStore, error) {
	if s == nil || s.schedules == nil {
		return nil, fmt.Errorf("broadcast schedule store is not configured")
	}
	return s.schedules, nil
}

func (s *Service) CreateSchedule(ctx context.Context, input ScheduleInput, actor string) (domain.BroadcastSchedule, error) {
	st, err := s.scheduleStore()
	if err != nil {
		return domain.BroadcastSchedule{}, err
	}
	preview, err := s.PreviewSchedule(input)
	if err != nil {
		return domain.BroadcastSchedule{}, err
	}
	return st.CreateBroadcastSchedule(ctx, preview.Schedule, strings.TrimSpace(actor))
}

// UpdateSchedule 整体替换计划的内容与规则，并从当前时刻重新算下次触发时间。
func (s *Service) UpdateSchedule(ctx context.Context, id int64, input ScheduleInput, actor string) (domain.BroadcastSchedule, error) {
	st, err := s.scheduleStore()
	if err != nil {
		return domain.BroadcastSchedule{}, err
	}
	if id <= 0 {
		return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
	}
	preview, err := s.PreviewSchedule(input)
	if err != nil {
		return domain.BroadcastSchedule{}, err
	}
	preview.Schedule.ID = id
	return st.UpdateBroadcastSchedule(ctx, preview.Schedule, strings.TrimSpace(actor))
}

func (s *Service) DeleteSchedule(ctx context.Context, id int64) error {
	st, err := s.scheduleStore()
	if err != nil {
		return err
	}
	if id <= 0 {
		return domain.ErrBroadcastScheduleNotFound
	}
	return st.DeleteBroadcastSchedule(ctx, id)
}

func (s *Service) GetSchedule(ctx context.Context, id int64) (domain.BroadcastSchedule, error) {
	st, err := s.scheduleStore()
	if err != nil {
		return domain.BroadcastSchedule{}, err
	}
	if id <= 0 {
		return domain.BroadcastSchedule{}, domain.ErrBroadcastScheduleNotFound
	}
	return st.GetBroadcastSchedule(ctx, id)
}

func (s *Service) ListSchedules(ctx context.Context) ([]domain.BroadcastSchedule, error) {
	st, err := s.scheduleStore()
	if err != nil {
		return nil, err
	}
	return st.ListBroadcastSchedules(ctx, domain.MaxBroadcastSchedules)
}

// PlanScheduleFiring 决定一次到期计划发送还是跳过，并算出下一次触发时间。
//
// 下一次一律从 now 往后算：停机很久之后恢复，只处理最近那一次，不会把错过的几次补出来。
func PlanScheduleFiring(due store.DueBroadcastSchedule, now time.Time) store.BroadcastScheduleFiring {
	schedule := due.Schedule
	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil || schedule.Timezone == "" || schedule.Timezone == "Local" {
		return store.BroadcastScheduleFiring{SkipReason: domain.BroadcastSkipInvalidTimezone}
	}
	next := NextScheduleRun(now, loc, schedule.TimeOfDay, schedule.Weekdays)
	switch {
	case now.Sub(schedule.NextRunAt) > domain.BroadcastScheduleMissedGrace:
		return store.BroadcastScheduleFiring{SkipReason: domain.BroadcastSkipMissed, NextRunAt: next}
	case due.PreviousRunning:
		return store.BroadcastScheduleFiring{SkipReason: domain.BroadcastSkipPreviousRunning, NextRunAt: next}
	}
	return store.BroadcastScheduleFiring{
		Entities:  domain.DetectAutomaticMessageEntities(schedule.Message, nil),
		NextRunAt: next,
	}
}

// FireDueSchedules 处理到期的自动广播计划，每条计划一个事务，最多 limit 条。
func (s *Service) FireDueSchedules(ctx context.Context, limit int) (created, skipped int, err error) {
	if s == nil || s.schedules == nil {
		return 0, 0, nil
	}
	if limit <= 0 {
		limit = scheduleFireBatch
	}
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return created, skipped, err
		}
		now := s.clock()
		outcome, ok, err := s.schedules.FireDueBroadcastSchedule(ctx, now, func(due store.DueBroadcastSchedule) store.BroadcastScheduleFiring {
			return PlanScheduleFiring(due, now)
		})
		if err != nil {
			return created, skipped, err
		}
		if !ok {
			return created, skipped, nil
		}
		if outcome.SkipReason != "" {
			skipped++
			s.log.Warn("scheduled broadcast skipped",
				zap.Int64("schedule_id", outcome.ScheduleID),
				zap.String("reason", string(outcome.SkipReason)),
				zap.Time("next_run_at", outcome.NextRunAt))
			continue
		}
		created++
		s.log.Info("scheduled broadcast created",
			zap.Int64("schedule_id", outcome.ScheduleID),
			zap.Int64("broadcast_id", outcome.BroadcastID),
			zap.Int64("target_count", outcome.TargetCount),
			zap.Time("next_run_at", outcome.NextRunAt))
	}
	return created, skipped, nil
}

func (s *Service) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}
