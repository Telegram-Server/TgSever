package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// BroadcastScheduleRow 是后台“自动广播”列表的一行。时间字段为 null 表示没有
// （停用的计划没有下次时间，没触发过的计划没有上次时间）。
type BroadcastScheduleRow struct {
	ID              int64 `json:"ID,string"`
	Message         string
	Timezone        string
	TimeOfDay       int
	Weekdays        []int
	Enabled         bool
	NextRunAt       *time.Time
	LastRunAt       *time.Time
	LastBroadcastID int64 `json:"LastBroadcastID,string"`
	RunCount        int64 `json:"RunCount,string"`
	LastSkippedAt   *time.Time
	LastSkipReason  string
	SkippedCount    int64 `json:"SkippedCount,string"`
	CreatedBy       string
	UpdatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (s *readStore) ListBroadcastSchedules(ctx context.Context) ([]BroadcastScheduleRow, error) {
	rows, err := sqlcgen.New(s.pool).ListBroadcastSchedules(ctx, domain.MaxBroadcastSchedules)
	if err != nil {
		return nil, fmt.Errorf("list broadcast schedules: %w", err)
	}
	out := make([]BroadcastScheduleRow, 0, len(rows))
	for _, row := range rows {
		item := BroadcastScheduleRow{
			ID:             row.ID,
			Message:        row.Message,
			Timezone:       row.Timezone,
			TimeOfDay:      int(row.TimeOfDay),
			Weekdays:       broadcastWeekdayList(domain.BroadcastWeekdays(row.Weekdays)),
			Enabled:        row.Enabled,
			RunCount:       row.RunCount,
			LastSkipReason: row.LastSkipReason,
			SkippedCount:   row.SkippedCount,
			CreatedBy:      row.CreatedBy,
			UpdatedBy:      row.UpdatedBy,
			CreatedAt:      row.CreatedAt.Time,
			UpdatedAt:      row.UpdatedAt.Time,
		}
		if row.NextRunAt.Valid {
			item.NextRunAt = &row.NextRunAt.Time
		}
		if row.LastRunAt.Valid {
			item.LastRunAt = &row.LastRunAt.Time
		}
		if row.LastSkippedAt.Valid {
			item.LastSkippedAt = &row.LastSkippedAt.Time
		}
		if row.LastBroadcastID != nil {
			item.LastBroadcastID = *row.LastBroadcastID
		}
		out = append(out, item)
	}
	return out, nil
}

func broadcastWeekdayList(mask domain.BroadcastWeekdays) []int {
	days := make([]int, 0, 7)
	for day := time.Sunday; day <= time.Saturday; day++ {
		if mask.Has(day) {
			days = append(days, int(day))
		}
	}
	return days
}

func (s *server) handleBroadcastSchedulesAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, err := s.read.ListBroadcastSchedules(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// broadcastScheduleAPIFields 是新建与修改计划共用的表单字段，含义见 admin.BroadcastScheduleFields。
type broadcastScheduleAPIFields struct {
	Message   string `json:"message"`
	Timezone  string `json:"timezone"`
	TimeOfDay int    `json:"time_of_day"`
	Weekdays  []int  `json:"weekdays"`
	Enabled   bool   `json:"enabled"`
}

func (f broadcastScheduleAPIFields) fields() admin.BroadcastScheduleFields {
	return admin.BroadcastScheduleFields{
		Message:   f.Message,
		Timezone:  f.Timezone,
		TimeOfDay: f.TimeOfDay,
		Weekdays:  f.Weekdays,
		Enabled:   f.Enabled,
	}
}

type createBroadcastScheduleAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	broadcastScheduleAPIFields
}

func (s *server) handleCreateBroadcastScheduleAPI(w http.ResponseWriter, r *http.Request) {
	var body createBroadcastScheduleAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.CreateBroadcastScheduleRequest{
		CommandMeta:             s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "create-broadcast-schedule"),
		BroadcastScheduleFields: body.fields(),
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/broadcasts/schedules/create", req)
	writeCommandResultAPI(w, result, err)
}

type updateBroadcastScheduleAPIRequest struct {
	CommandID  string `json:"command_id"`
	Reason     string `json:"reason"`
	Confirm    bool   `json:"confirm"`
	ScheduleID int64  `json:"schedule_id,string"`
	broadcastScheduleAPIFields
}

func (s *server) handleUpdateBroadcastScheduleAPI(w http.ResponseWriter, r *http.Request) {
	var body updateBroadcastScheduleAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.UpdateBroadcastScheduleRequest{
		CommandMeta:             s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "update-broadcast-schedule"),
		ScheduleID:              body.ScheduleID,
		BroadcastScheduleFields: body.fields(),
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/broadcasts/schedules/update", req)
	writeCommandResultAPI(w, result, err)
}

type deleteBroadcastScheduleAPIRequest struct {
	CommandID  string `json:"command_id"`
	Reason     string `json:"reason"`
	Confirm    bool   `json:"confirm"`
	ScheduleID int64  `json:"schedule_id,string"`
}

func (s *server) handleDeleteBroadcastScheduleAPI(w http.ResponseWriter, r *http.Request) {
	var body deleteBroadcastScheduleAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.DeleteBroadcastScheduleRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "delete-broadcast-schedule"),
		ScheduleID:  body.ScheduleID,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/broadcasts/schedules/delete", req)
	writeCommandResultAPI(w, result, err)
}
