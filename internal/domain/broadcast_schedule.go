package domain

import (
	"errors"
	"time"
)

// BroadcastSchedule 是自动广播计划：每到触发时刻，按计划内容给全部账号建一条普通广播。
//
// 触发规则是“星期 + 当地时刻 + 时区”。Weekdays 是按 time.Weekday 编号的位掩码
// （bit0 = 周日 … bit6 = 周六），TimeOfDay 是当地时刻在一天中的分钟数。
type BroadcastSchedule struct {
	ID        int64
	Message   string
	Timezone  string
	TimeOfDay int
	Weekdays  BroadcastWeekdays
	Enabled   bool
	// NextRunAt 为零值表示计划已停用。
	NextRunAt       time.Time
	LastRunAt       time.Time
	LastBroadcastID int64
	RunCount        int64
	LastSkippedAt   time.Time
	LastSkipReason  BroadcastSkipReason
	SkippedCount    int64
	CreatedBy       string
	UpdatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// BroadcastWeekdays 是星期位掩码，bit(time.Weekday) 置位表示那天触发。
type BroadcastWeekdays uint8

const BroadcastEveryDay BroadcastWeekdays = 0x7f

func BroadcastWeekdaysOf(days ...time.Weekday) BroadcastWeekdays {
	var mask BroadcastWeekdays
	for _, day := range days {
		mask |= 1 << uint(day)
	}
	return mask
}

func (w BroadcastWeekdays) Has(day time.Weekday) bool {
	return day >= time.Sunday && day <= time.Saturday && w&(1<<uint(day)) != 0
}

func (w BroadcastWeekdays) Valid() bool {
	return w > 0 && w <= BroadcastEveryDay
}

// BroadcastSkipReason 记录计划某次到点没有发出广播的原因。
type BroadcastSkipReason string

const (
	// BroadcastSkipPreviousRunning：同一计划上一次的广播还没投递完。
	BroadcastSkipPreviousRunning BroadcastSkipReason = "previous_running"
	// BroadcastSkipMissed：服务停机等原因，到点后超过宽限期才轮到，不再补发。
	BroadcastSkipMissed BroadcastSkipReason = "missed"
	// BroadcastSkipNoRecipients：此刻没有任何可接收广播的账号。
	BroadcastSkipNoRecipients BroadcastSkipReason = "no_recipients"
	// BroadcastSkipInvalidTimezone：保存的时区在本机时区库里找不到，计划被停用。
	BroadcastSkipInvalidTimezone BroadcastSkipReason = "invalid_timezone"
)

const (
	// BroadcastScheduleMissedGrace：到点后在这段时间内轮到仍然发送（重启、worker 间隔），
	// 超过就算错过，只推进到下一次。
	BroadcastScheduleMissedGrace = time.Hour
	MaxBroadcastSchedules        = 200
	DefaultBroadcastTimezone     = "Asia/Shanghai"
)

var (
	ErrBroadcastScheduleNotFound    = errors.New("broadcast schedule not found")
	ErrBroadcastScheduleInvalidTime = errors.New("broadcast schedule time invalid")
	ErrBroadcastScheduleInvalidDays = errors.New("broadcast schedule weekdays invalid")
	ErrBroadcastScheduleInvalidZone = errors.New("broadcast schedule timezone invalid")
	ErrBroadcastScheduleLimit       = errors.New("broadcast schedule limit reached")
)
