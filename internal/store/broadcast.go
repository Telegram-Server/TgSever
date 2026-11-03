package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

type BroadcastStore interface {
	PreviewBroadcastRecipients(ctx context.Context, mode domain.BroadcastTargetMode, selectedUserIDs []int64) (int64, error)
	CreateBroadcast(ctx context.Context, message string, entities []domain.MessageEntity, mode domain.BroadcastTargetMode, selectedUserIDs []int64, createdBy string) (domain.Broadcast, error)
	MaterializeBroadcastRecipients(ctx context.Context, limit int) (int, error)
	ClaimBroadcastRecipients(ctx context.Context, leaseToken string, limit int, lease time.Duration) ([]BroadcastRecipientClaim, error)
	ReleaseBroadcastRecipient(ctx context.Context, claim BroadcastRecipientClaim, cause string) error
	ListBroadcasts(ctx context.Context, beforeID int64, limit int) ([]domain.Broadcast, bool, error)
}

type BroadcastDeliveryStore interface {
	DeliverBroadcastRecipient(ctx context.Context, claim BroadcastRecipientClaim) (domain.Message, error)
}

type BroadcastRecipientClaim struct {
	RecipientID int64
	BroadcastID int64
	UserID      int64
	Attempts    int
	LeaseToken  string
}

// BroadcastScheduleStore 保存自动广播计划，并在一个事务里完成“锁住到期计划 → 建广播 →
// 推进下次触发时间”，所以多实例与重启都不会让同一次触发发两遍。
type BroadcastScheduleStore interface {
	CreateBroadcastSchedule(ctx context.Context, schedule domain.BroadcastSchedule, actor string) (domain.BroadcastSchedule, error)
	UpdateBroadcastSchedule(ctx context.Context, schedule domain.BroadcastSchedule, actor string) (domain.BroadcastSchedule, error)
	DeleteBroadcastSchedule(ctx context.Context, id int64) error
	GetBroadcastSchedule(ctx context.Context, id int64) (domain.BroadcastSchedule, error)
	ListBroadcastSchedules(ctx context.Context, limit int) ([]domain.BroadcastSchedule, error)
	// FireDueBroadcastSchedule 锁住一条 next_run_at <= now 的计划，交给 plan 决定本次
	// 发送还是跳过、下次何时触发，然后在同一事务里落库。没有到期计划时 ok 为 false。
	FireDueBroadcastSchedule(ctx context.Context, now time.Time, plan BroadcastSchedulePlanner) (outcome BroadcastScheduleOutcome, ok bool, err error)
}

// DueBroadcastSchedule 是锁住的到期计划，附带决定本次是否发送所需的状态。
type DueBroadcastSchedule struct {
	Schedule domain.BroadcastSchedule
	// PreviousRunning：同一计划之前的广播还在枚举或投递。
	PreviousRunning bool
}

// BroadcastScheduleFiring 是 planner 对一次到期的决定。
type BroadcastScheduleFiring struct {
	// SkipReason 为空表示发送。
	SkipReason domain.BroadcastSkipReason
	Entities   []domain.MessageEntity
	// NextRunAt 为零值时计划被停用（例如时区失效）。
	NextRunAt time.Time
}

type BroadcastSchedulePlanner func(DueBroadcastSchedule) BroadcastScheduleFiring

type BroadcastScheduleOutcome struct {
	ScheduleID  int64
	BroadcastID int64
	TargetCount int64
	SkipReason  domain.BroadcastSkipReason
	NextRunAt   time.Time
}
