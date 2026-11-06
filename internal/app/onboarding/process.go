package onboarding

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/domain"
)

// defaultRetryBackoff 是任务失败后再次执行前的等待基数：第 n 次失败后等 n 倍。
const defaultRetryBackoff = 30 * time.Second

// Publisher 把一次执行落下的变化推送给在线客户端（生产里是 rpc 调度器）。执行失败时也会带着
// 失败前已经落下的部分调用一次。
type Publisher func(ctx context.Context, job domain.OnboardingJob, res Result)

// WithRetryBackoff 设置失败重试的等待基数（测试用 0 立即重试）。
func (s *Service) WithRetryBackoff(base time.Duration) *Service {
	s.retryBackoff = base
	return s
}

// ProcessPending 领取最多 limit 条到期的任务并逐条执行：成功的删除；失败的记下原因并在退避后
// 重试，累计失败 domain.OnboardingJobMaxAttempts 次后放弃。每条任务执行后都调用一次 publish
// （可以为 nil）。返回领取到的任务数。
func (s *Service) ProcessPending(ctx context.Context, limit int, lease time.Duration, publish Publisher) (int, error) {
	jobs, err := s.store.ClaimOnboardingJobs(ctx, limit, lease)
	if err != nil {
		return 0, fmt.Errorf("onboarding: claim jobs: %w", err)
	}
	for _, job := range jobs {
		res, execErr := s.Execute(ctx, job)
		if publish != nil {
			publish(ctx, job, res)
		}
		if err := s.settle(ctx, job, execErr); err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

// settle 按执行结果删除、重试或放弃任务。
func (s *Service) settle(ctx context.Context, job domain.OnboardingJob, execErr error) error {
	var err error
	switch failures := job.Attempts + 1; {
	case execErr == nil:
		err = s.store.CompleteOnboardingJob(ctx, job.UserID)
	case failures >= domain.OnboardingJobMaxAttempts:
		err = s.store.AbandonOnboardingJob(ctx, job.UserID, execErr.Error())
	default:
		err = s.store.RetryOnboardingJob(ctx, job.UserID, execErr.Error(), time.Duration(failures)*s.retryBackoff)
	}
	if err != nil {
		return fmt.Errorf("onboarding: settle job of %d: %w", job.UserID, err)
	}
	return nil
}
