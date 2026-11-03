package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	broadcastapp "telesrv/internal/app/broadcast"
	"telesrv/internal/domain"
)

// 自动广播计划的三条命令。后台路由由 broadcasts.schedule 把关；走普通的命令通道：
// 试运行（校验并给出接下来几次触发时间）、按 command id 幂等、写审计。
const (
	ActionCreateBroadcastSchedule = "broadcast.schedule.create"
	ActionUpdateBroadcastSchedule = "broadcast.schedule.update"
	ActionDeleteBroadcastSchedule = "broadcast.schedule.delete"
)

const (
	CodeBroadcastScheduleNotFound = "BROADCAST_SCHEDULE_NOT_FOUND"
	CodeBroadcastScheduleInvalid  = "BROADCAST_SCHEDULE_INVALID"
	CodeBroadcastScheduleLimit    = "BROADCAST_SCHEDULE_LIMIT"
)

// BroadcastScheduleService 是自动广播计划用到的业务服务（app/broadcast.Service 的子集）。
type BroadcastScheduleService interface {
	PreviewSchedule(input broadcastapp.ScheduleInput) (broadcastapp.SchedulePreview, error)
	CreateSchedule(ctx context.Context, input broadcastapp.ScheduleInput, actor string) (domain.BroadcastSchedule, error)
	UpdateSchedule(ctx context.Context, id int64, input broadcastapp.ScheduleInput, actor string) (domain.BroadcastSchedule, error)
	DeleteSchedule(ctx context.Context, id int64) error
	GetSchedule(ctx context.Context, id int64) (domain.BroadcastSchedule, error)
}

// BroadcastScheduleFields 是计划的内容与触发规则。TimeOfDay 是当地时刻的分钟数
// （14:00 = 840）；Weekdays 是触发的星期，0 = 周日 … 6 = 周六，七天都选即每天。
// Timezone 为空时取 Asia/Shanghai。自动广播一律发给全部账号。
type BroadcastScheduleFields struct {
	Message   string `json:"message"`
	Timezone  string `json:"timezone"`
	TimeOfDay int    `json:"time_of_day"`
	Weekdays  []int  `json:"weekdays"`
	Enabled   bool   `json:"enabled"`
}

type CreateBroadcastScheduleRequest struct {
	CommandMeta
	BroadcastScheduleFields
}

type UpdateBroadcastScheduleRequest struct {
	CommandMeta
	ScheduleID int64 `json:"schedule_id,string"`
	BroadcastScheduleFields
}

type DeleteBroadcastScheduleRequest struct {
	CommandMeta
	ScheduleID int64 `json:"schedule_id,string"`
}

func (f BroadcastScheduleFields) input() (broadcastapp.ScheduleInput, error) {
	var mask domain.BroadcastWeekdays
	for _, day := range f.Weekdays {
		if day < int(time.Sunday) || day > int(time.Saturday) {
			return broadcastapp.ScheduleInput{}, domain.ErrBroadcastScheduleInvalidDays
		}
		mask |= domain.BroadcastWeekdaysOf(time.Weekday(day))
	}
	return broadcastapp.ScheduleInput{
		Message:   f.Message,
		Timezone:  f.Timezone,
		TimeOfDay: f.TimeOfDay,
		Weekdays:  mask,
		Enabled:   f.Enabled,
	}, nil
}

func (s *Service) CreateBroadcastSchedule(ctx context.Context, req CreateBroadcastScheduleRequest) (CommandResult, error) {
	if s == nil || s.broadcastSchedules == nil {
		return CommandResult{}, fmt.Errorf("admin broadcast schedule dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionCreateBroadcastSchedule, 0, domain.Peer{}, req, func() (CommandResult, error) {
		input, details, err := s.previewBroadcastSchedule(ctx, req.BroadcastScheduleFields)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "broadcast schedule validated", Details: details}, nil
		}
		created, err := s.broadcastSchedules.CreateSchedule(ctx, input, req.Actor)
		if err != nil {
			return CommandResult{Details: details}, broadcastScheduleError(err)
		}
		details["schedule_id"] = strconv.FormatInt(created.ID, 10)
		return CommandResult{Message: "broadcast schedule created", Details: details}, nil
	})
}

func (s *Service) UpdateBroadcastSchedule(ctx context.Context, req UpdateBroadcastScheduleRequest) (CommandResult, error) {
	if req.ScheduleID <= 0 {
		return CommandResult{}, fmt.Errorf("schedule_id is required")
	}
	if s == nil || s.broadcastSchedules == nil {
		return CommandResult{}, fmt.Errorf("admin broadcast schedule dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionUpdateBroadcastSchedule, 0, domain.Peer{}, req, func() (CommandResult, error) {
		current, err := s.broadcastSchedules.GetSchedule(ctx, req.ScheduleID)
		if err != nil {
			return CommandResult{}, broadcastScheduleError(err)
		}
		input, details, err := s.previewBroadcastSchedule(ctx, req.BroadcastScheduleFields)
		details["schedule_id"] = strconv.FormatInt(req.ScheduleID, 10)
		details["previous_enabled"] = current.Enabled
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "broadcast schedule validated", Details: details}, nil
		}
		if _, err := s.broadcastSchedules.UpdateSchedule(ctx, req.ScheduleID, input, req.Actor); err != nil {
			return CommandResult{Details: details}, broadcastScheduleError(err)
		}
		return CommandResult{Message: "broadcast schedule updated", Details: details}, nil
	})
}

func (s *Service) DeleteBroadcastSchedule(ctx context.Context, req DeleteBroadcastScheduleRequest) (CommandResult, error) {
	if req.ScheduleID <= 0 {
		return CommandResult{}, fmt.Errorf("schedule_id is required")
	}
	if s == nil || s.broadcastSchedules == nil {
		return CommandResult{}, fmt.Errorf("admin broadcast schedule dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteBroadcastSchedule, 0, domain.Peer{}, req, func() (CommandResult, error) {
		current, err := s.broadcastSchedules.GetSchedule(ctx, req.ScheduleID)
		if err != nil {
			return CommandResult{}, broadcastScheduleError(err)
		}
		details := map[string]any{
			"schedule_id":     strconv.FormatInt(current.ID, 10),
			"message_preview": truncateBroadcastPreview(current.Message),
			"enabled":         current.Enabled,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		if err := s.broadcastSchedules.DeleteSchedule(ctx, req.ScheduleID); err != nil {
			return CommandResult{Details: details}, broadcastScheduleError(err)
		}
		return CommandResult{Message: "broadcast schedule deleted", Details: details}, nil
	})
}

// previewBroadcastSchedule 校验计划并组装试运行展示的字段：规范化后的内容、
// 接下来几次触发时间，以及此刻会收到广播的账号数。
func (s *Service) previewBroadcastSchedule(ctx context.Context, fields BroadcastScheduleFields) (broadcastapp.ScheduleInput, map[string]any, error) {
	details := map[string]any{}
	input, err := fields.input()
	if err != nil {
		return broadcastapp.ScheduleInput{}, details, broadcastScheduleError(err)
	}
	preview, err := s.broadcastSchedules.PreviewSchedule(input)
	if err != nil {
		return broadcastapp.ScheduleInput{}, details, broadcastScheduleError(err)
	}
	schedule := preview.Schedule
	upcoming := make([]string, 0, len(preview.Upcoming))
	for _, at := range preview.Upcoming {
		upcoming = append(upcoming, at.Format(time.RFC3339))
	}
	details["message_preview"] = truncateBroadcastPreview(schedule.Message)
	details["timezone"] = schedule.Timezone
	details["time_of_day"] = fmt.Sprintf("%02d:%02d", schedule.TimeOfDay/60, schedule.TimeOfDay%60)
	details["weekdays"] = weekdayList(schedule.Weekdays)
	details["enabled"] = schedule.Enabled
	details["upcoming"] = upcoming
	if s.broadcast != nil {
		if count, err := s.broadcast.Preview(ctx, schedule.Message, domain.BroadcastTargetAll, nil); err == nil {
			details["recipient_count_now"] = count
		}
	}
	input.Message = schedule.Message
	input.Timezone = schedule.Timezone
	return input, details, nil
}

func weekdayList(mask domain.BroadcastWeekdays) []int {
	days := make([]int, 0, 7)
	for day := time.Sunday; day <= time.Saturday; day++ {
		if mask.Has(day) {
			days = append(days, int(day))
		}
	}
	return days
}

func broadcastScheduleError(err error) error {
	switch {
	case errors.Is(err, domain.ErrBroadcastScheduleNotFound):
		return codedError(CodeBroadcastScheduleNotFound, err)
	case errors.Is(err, domain.ErrBroadcastScheduleLimit):
		return codedError(CodeBroadcastScheduleLimit, err)
	case errors.Is(err, domain.ErrBroadcastScheduleInvalidTime),
		errors.Is(err, domain.ErrBroadcastScheduleInvalidDays),
		errors.Is(err, domain.ErrBroadcastScheduleInvalidZone),
		errors.Is(err, domain.ErrBroadcastMessageEmpty),
		errors.Is(err, domain.ErrBroadcastMessageTooLong):
		return codedError(CodeBroadcastScheduleInvalid, err)
	}
	return err
}
