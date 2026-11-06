package memory

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// OnboardingStore 是 store.OnboardingStore 的内存实现：模板与任务共用一把锁。建号时由
// AccountCreationStore（WithOnboarding）在持有 users.mu 期间调用 enqueueLocked 写入任务。
type OnboardingStore struct {
	mu          sync.Mutex
	defaults    domain.OnboardingDefaults
	hasDefaults bool
	jobs        map[int64]*onboardingJobRecord
	seq         int64
}

// onboardingJobRecord 是一条任务及其调度状态：availableAt 同时充当租约与重试时间，seq 是写入顺序。
type onboardingJobRecord struct {
	job         domain.OnboardingJob
	seq         int64
	availableAt time.Time
	abandoned   bool
}

var _ store.OnboardingStore = (*OnboardingStore)(nil)

// NewOnboardingStore 创建空的入驻 store：没有模板，也没有任务。
func NewOnboardingStore() *OnboardingStore {
	return &OnboardingStore{jobs: make(map[int64]*onboardingJobRecord)}
}

func (s *OnboardingStore) GetOnboardingDefaults(context.Context) (domain.OnboardingDefaults, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasDefaults {
		return domain.OnboardingDefaults{}, false, nil
	}
	return s.defaults.Clone(), true, nil
}

func (s *OnboardingStore) SetOnboardingDefaults(_ context.Context, d domain.OnboardingDefaults) (domain.OnboardingDefaults, bool, domain.OnboardingDefaults, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prev domain.OnboardingDefaults
	if s.hasDefaults {
		prev = s.defaults.Clone()
	}
	prevFound := s.hasDefaults
	written := d.Clone()
	written.UpdatedAt = time.Now().UTC()
	s.defaults, s.hasDefaults = written.Clone(), true
	return prev, prevFound, written, nil
}

// enqueueLocked 按当前模板为新账号写一条任务；模板为空时什么都不写。调用方持有 s.mu。
func (s *OnboardingStore) enqueueLocked(userID int64, now time.Time) {
	if !s.hasDefaults || s.defaults.Empty() {
		return
	}
	snapshot := s.defaults.Clone()
	s.seq++
	s.jobs[userID] = &onboardingJobRecord{
		job:         domain.OnboardingJob{UserID: userID, Contacts: snapshot.Contacts, ChannelIDs: snapshot.ChannelIDs, CreatedAt: now},
		seq:         s.seq,
		availableAt: now,
	}
}

func (s *OnboardingStore) ClaimOnboardingJobs(_ context.Context, limit int, lease time.Duration) ([]domain.OnboardingJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	claimable := make([]*onboardingJobRecord, 0, len(s.jobs))
	for _, r := range s.jobs {
		if !r.abandoned && !r.availableAt.After(now) {
			claimable = append(claimable, r)
		}
	}
	slices.SortFunc(claimable, func(a, b *onboardingJobRecord) int { return cmp.Compare(a.seq, b.seq) })
	if len(claimable) > limit {
		claimable = claimable[:limit]
	}
	out := make([]domain.OnboardingJob, 0, len(claimable))
	for _, r := range claimable {
		r.availableAt = now.Add(lease)
		out = append(out, cloneOnboardingJob(r.job))
	}
	return out, nil
}

func (s *OnboardingStore) CompleteOnboardingJob(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, userID)
	return nil
}

func (s *OnboardingStore) RetryOnboardingJob(_ context.Context, userID int64, lastError string, retryAfter time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.jobs[userID]; ok {
		r.job.Attempts++
		r.job.LastError = lastError
		r.availableAt = time.Now().Add(retryAfter)
	}
	return nil
}

func (s *OnboardingStore) AbandonOnboardingJob(_ context.Context, userID int64, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.jobs[userID]; ok {
		r.job.Attempts++
		r.job.LastError = lastError
		r.abandoned = true
	}
	return nil
}

func cloneOnboardingJob(j domain.OnboardingJob) domain.OnboardingJob {
	j.Contacts = slices.Clone(j.Contacts)
	j.ChannelIDs = slices.Clone(j.ChannelIDs)
	return j
}
