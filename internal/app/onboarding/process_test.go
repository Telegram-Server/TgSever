package onboarding_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"telesrv/internal/app/channels"
	"telesrv/internal/app/onboarding"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// enqueue 用模板为 newcomer 写一条任务（经建号写入，与生产一致）。
func (f *fixture) enqueue(t *testing.T, tmpl domain.OnboardingDefaults) domain.User {
	t.Helper()
	ctx := context.Background()
	if _, _, _, err := f.store.SetOnboardingDefaults(ctx, tmpl); err != nil {
		t.Fatal(err)
	}
	accounts := memoryAccounts(f)
	u, err := accounts.CreateAccount(ctx, domain.User{AccessHash: 9, FirstName: "Newcomer", Phone: f.nextPhone()}, store.AccountExtras{})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return u
}

func TestProcessPendingExecutesPublishesAndCompletesJobs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	support := f.newUser(t, "Support")
	newcomer := f.enqueue(t, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: support.ID}}})

	var published []int64
	n, err := f.svc.ProcessPending(ctx, 10, time.Minute, func(_ context.Context, job domain.OnboardingJob, res onboarding.Result) {
		published = append(published, job.UserID)
		if len(res.Contacts) != 1 {
			t.Errorf("published result = %+v, want the added contact", res)
		}
	})
	if err != nil || n != 1 {
		t.Fatalf("process n=%d err=%v, want 1 job", n, err)
	}
	if !slices.Equal(published, []int64{newcomer.ID}) {
		t.Fatalf("published = %v, want [%d]", published, newcomer.ID)
	}
	if got := f.contactIDs(t, newcomer.ID); !slices.Equal(got, []int64{support.ID}) {
		t.Fatalf("newcomer contacts = %v", got)
	}
	if n, err := f.svc.ProcessPending(ctx, 10, 0, nil); err != nil || n != 0 {
		t.Fatalf("second process n=%d err=%v, want the job completed and gone", n, err)
	}
}

// flakyChannels 让加入群总是遇到一个暂时性错误。
type flakyChannels struct{ onboarding.Channels }

var errTransient = errors.New("database is restarting")

func (flakyChannels) AdmitToChannel(context.Context, int64, int64, int) (domain.CreateChannelResult, error) {
	return domain.CreateChannelResult{}, errTransient
}

func TestProcessPendingRetriesAFailingJobThenAbandonsIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, support := f.newUser(t, "Owner"), f.newUser(t, "Support")
	group := f.newGroup(t, owner.ID)
	newcomer := f.enqueue(t, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: support.ID}}, ChannelIDs: []int64{group}})
	svc := onboarding.NewService(f.store, f.users, f.contacts, flakyChannels{channels.NewService(f.channels)}).WithRetryBackoff(0)

	var publishedContacts int
	for attempt := 1; attempt <= domain.OnboardingJobMaxAttempts; attempt++ {
		n, err := svc.ProcessPending(ctx, 10, time.Minute, func(_ context.Context, _ domain.OnboardingJob, res onboarding.Result) {
			publishedContacts += len(res.Contacts)
		})
		if err != nil || n != 1 {
			t.Fatalf("attempt %d: process n=%d err=%v, want the job claimed again", attempt, n, err)
		}
	}
	// 失败前已经落下的变化照样推送（联系人每次都写入，update 每次都发）。
	if publishedContacts != domain.OnboardingJobMaxAttempts {
		t.Fatalf("published contacts = %d, want one per attempt", publishedContacts)
	}
	if n, err := svc.ProcessPending(ctx, 10, time.Minute, nil); err != nil || n != 0 {
		t.Fatalf("after %d failures process n=%d err=%v, want the job abandoned", domain.OnboardingJobMaxAttempts, n, err)
	}
	if got := f.contactIDs(t, newcomer.ID); !slices.Equal(got, []int64{support.ID}) {
		t.Fatalf("newcomer contacts = %v, want the contact added before the failure", got)
	}
}
