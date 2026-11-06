package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// OnboardingStore 保存入驻模板与入驻任务。memory 与 postgres 实现同一份契约
// （storetest.OnboardingContract）。
//
// 入驻任务只由 AccountCreationStore.CreateAccount 写入：建号事务里读当时的模板，模板不为空就为
// 新账号写一条任务，与账号一起提交或回滚。
type OnboardingStore interface {
	// GetOnboardingDefaults 返回模板，名单按配置顺序；found=false 表示还没有设置过。
	GetOnboardingDefaults(ctx context.Context) (d domain.OnboardingDefaults, found bool, err error)
	// SetOnboardingDefaults 整体替换模板（两份名单都整份替换），UpdatedAt 记为写入时间，CommandID
	// 原样保存，返回替换前的值（没有时 prevFound=false）与写入的值。名单里的账号、群和频道必须
	// 存在且不重复，由调用方校验。
	SetOnboardingDefaults(ctx context.Context, d domain.OnboardingDefaults) (prev domain.OnboardingDefaults, prevFound bool, written domain.OnboardingDefaults, err error)

	// ClaimOnboardingJobs 领取最多 limit 条可执行的任务并加上 lease 时长的租约：租约内其他领取者
	// 看不到它们，租约过期（执行者崩溃）后可以再被领取。按建号先后领取。
	ClaimOnboardingJobs(ctx context.Context, limit int, lease time.Duration) ([]domain.OnboardingJob, error)
	// CompleteOnboardingJob 删除执行完的任务。任务不存在时不报错。
	CompleteOnboardingJob(ctx context.Context, userID int64) error
	// RetryOnboardingJob 记一次失败（Attempts 加一、记下 lastError），retryAfter 之后才能再被领取。
	RetryOnboardingJob(ctx context.Context, userID int64, lastError string, retryAfter time.Duration) error
	// AbandonOnboardingJob 记一次失败并放弃任务：之后不再被领取。
	AbandonOnboardingJob(ctx context.Context, userID int64, lastError string) error
}
