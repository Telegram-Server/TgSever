package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// OnboardingStore 是 store.OnboardingStore 的 postgres 实现：模板是 account_onboarding_defaults
// 唯一的一行加两张名单表，任务在 account_onboarding_jobs。任务由 AccountCreationStore.CreateAccount
// 在建号事务里写入（enqueueOnboardingJobTx）。
type OnboardingStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

var _ store.OnboardingStore = (*OnboardingStore)(nil)

// NewOnboardingStore 创建入驻 store。db 必须支持事务（*pgxpool.Pool）。
func NewOnboardingStore(db sqlcgen.DBTX) *OnboardingStore {
	return &OnboardingStore{db: db, q: sqlcgen.New(db)}
}

func (s *OnboardingStore) GetOnboardingDefaults(ctx context.Context) (domain.OnboardingDefaults, bool, error) {
	return getOnboardingDefaults(ctx, s.q)
}

func getOnboardingDefaults(ctx context.Context, q *sqlcgen.Queries) (domain.OnboardingDefaults, bool, error) {
	row, err := q.GetOnboardingDefaults(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.OnboardingDefaults{}, false, nil
		}
		return domain.OnboardingDefaults{}, false, fmt.Errorf("get onboarding defaults: %w", err)
	}
	return domain.OnboardingDefaults{
		Contacts:   onboardingContacts(row.ContactUserIds, row.ContactMutual),
		ChannelIDs: row.ChannelIds,
		UpdatedAt:  row.UpdatedAt.Time,
		CommandID:  row.CommandID,
	}.Clone(), true, nil
}

func (s *OnboardingStore) SetOnboardingDefaults(ctx context.Context, d domain.OnboardingDefaults) (prev domain.OnboardingDefaults, prevFound bool, written domain.OnboardingDefaults, err error) {
	err = withTx(ctx, s.db, "set onboarding defaults", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		// 表锁让并发的整体替换排队（含两次首次设置）；之后读到的就是锁内的当前值。
		if err := q.LockOnboardingDefaultsTable(ctx); err != nil {
			return err
		}
		var err error
		if prev, prevFound, err = getOnboardingDefaults(ctx, q); err != nil {
			return err
		}
		next := d.Clone()
		updatedAt, err := q.UpsertOnboardingDefaults(ctx, next.CommandID)
		if err != nil {
			return err
		}
		next.UpdatedAt = updatedAt.Time
		if err := q.DeleteOnboardingDefaultContacts(ctx); err != nil {
			return err
		}
		if len(next.Contacts) > 0 {
			ids, mutual := splitOnboardingContacts(next.Contacts)
			if err := q.InsertOnboardingDefaultContacts(ctx, sqlcgen.InsertOnboardingDefaultContactsParams{ContactUserIds: ids, Mutual: mutual}); err != nil {
				return err
			}
		}
		if err := q.DeleteOnboardingDefaultChannels(ctx); err != nil {
			return err
		}
		if len(next.ChannelIDs) > 0 {
			if err := q.InsertOnboardingDefaultChannels(ctx, next.ChannelIDs); err != nil {
				return err
			}
		}
		written = next
		return nil
	})
	if err != nil {
		return domain.OnboardingDefaults{}, false, domain.OnboardingDefaults{}, fmt.Errorf("set onboarding defaults: %w", err)
	}
	return prev, prevFound, written, nil
}

// enqueueOnboardingJobTx 在建号事务里按当时的模板为新账号写一条任务；模板为空时不写。
func enqueueOnboardingJobTx(ctx context.Context, q *sqlcgen.Queries, userID int64) error {
	if err := q.EnqueueOnboardingJob(ctx, userID); err != nil {
		return fmt.Errorf("enqueue onboarding job: %w", err)
	}
	return nil
}

func (s *OnboardingStore) ClaimOnboardingJobs(ctx context.Context, limit int, lease time.Duration) ([]domain.OnboardingJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.q.ClaimOnboardingJobs(ctx, sqlcgen.ClaimOnboardingJobsParams{LeaseSeconds: lease.Seconds(), MaxJobs: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("claim onboarding jobs: %w", err)
	}
	jobs := make([]domain.OnboardingJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, domain.OnboardingJob{
			UserID:     row.UserID,
			Contacts:   onboardingContacts(row.ContactUserIds, row.ContactMutual),
			ChannelIDs: append([]int64{}, row.ChannelIds...),
			Attempts:   int(row.Attempts),
			LastError:  row.LastError,
			CreatedAt:  row.CreatedAt.Time,
		})
	}
	// UPDATE … RETURNING 不保证顺序，按建号先后排回来。
	slices.SortFunc(jobs, func(a, b domain.OnboardingJob) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
	return jobs, nil
}

func (s *OnboardingStore) CompleteOnboardingJob(ctx context.Context, userID int64) error {
	if err := s.q.DeleteOnboardingJob(ctx, userID); err != nil {
		return fmt.Errorf("complete onboarding job: %w", err)
	}
	return nil
}

func (s *OnboardingStore) RetryOnboardingJob(ctx context.Context, userID int64, lastError string, retryAfter time.Duration) error {
	if err := s.q.RetryOnboardingJob(ctx, sqlcgen.RetryOnboardingJobParams{UserID: userID, LastError: lastError, RetryAfterSeconds: retryAfter.Seconds()}); err != nil {
		return fmt.Errorf("retry onboarding job: %w", err)
	}
	return nil
}

func (s *OnboardingStore) AbandonOnboardingJob(ctx context.Context, userID int64, lastError string) error {
	if err := s.q.AbandonOnboardingJob(ctx, sqlcgen.AbandonOnboardingJobParams{UserID: userID, LastError: lastError}); err != nil {
		return fmt.Errorf("abandon onboarding job: %w", err)
	}
	return nil
}

func onboardingContacts(ids []int64, mutual []bool) []domain.OnboardingContact {
	out := make([]domain.OnboardingContact, len(ids))
	for i, id := range ids {
		out[i] = domain.OnboardingContact{UserID: id, Mutual: i < len(mutual) && mutual[i]}
	}
	return out
}

func splitOnboardingContacts(contacts []domain.OnboardingContact) ([]int64, []bool) {
	ids := make([]int64, len(contacts))
	mutual := make([]bool, len(contacts))
	for i, c := range contacts {
		ids[i], mutual[i] = c.UserID, c.Mutual
	}
	return ids, mutual
}
