package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// OnboardingFixture 是同一份底层数据上的建号操作与入驻 store，开始时没有模板也没有任务。
// NewUser 建一个普通账号；NewChannel 以 owner 为创建者建一个超级群并返回它的 id。
type OnboardingFixture struct {
	Accounts   store.AccountCreationStore
	Onboarding store.OnboardingStore
	NewUser    func(t *testing.T) domain.User
	NewPhone   func(t *testing.T) string
	NewChannel func(t *testing.T, ownerUserID int64) int64
}

// OnboardingContract 是入驻模板与入驻任务的存储契约。
func OnboardingContract(t *testing.T, newFixture func(t *testing.T) OnboardingFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("no template until the first write", func(t *testing.T) {
		f := newFixture(t)
		if _, found, err := f.Onboarding.GetOnboardingDefaults(ctx); err != nil || found {
			t.Fatalf("get template before any write found=%v err=%v, want not found", found, err)
		}
	})

	t.Run("set writes then replaces the whole template keeping the configured order", func(t *testing.T) {
		f := newFixture(t)
		a, b, c := f.NewUser(t), f.NewUser(t), f.NewUser(t)
		ch1, ch2 := f.NewChannel(t, a.ID), f.NewChannel(t, a.ID)
		first := domain.OnboardingDefaults{
			Contacts:   []domain.OnboardingContact{{UserID: c.ID, Mutual: true}, {UserID: a.ID}},
			ChannelIDs: []int64{ch2, ch1},
			CommandID:  "onboarding-1",
		}
		prev, found, written, err := f.Onboarding.SetOnboardingDefaults(ctx, first)
		if err != nil || found || !reflect.DeepEqual(prev, domain.OnboardingDefaults{}) {
			t.Fatalf("first set prev=%+v found=%v err=%v, want no previous template", prev, found, err)
		}
		if written.UpdatedAt.IsZero() {
			t.Fatal("written UpdatedAt is zero")
		}
		assertOnboardingDefaults(t, written, first)
		assertStoredOnboardingDefaults(t, f, first)

		second := domain.OnboardingDefaults{
			Contacts:  []domain.OnboardingContact{{UserID: b.ID}},
			CommandID: "onboarding-2",
		}
		prev, found, _, err = f.Onboarding.SetOnboardingDefaults(ctx, second)
		if err != nil || !found {
			t.Fatalf("second set found=%v err=%v", found, err)
		}
		assertOnboardingDefaults(t, prev, first)
		assertStoredOnboardingDefaults(t, f, second)

		cleared := domain.OnboardingDefaults{CommandID: "onboarding-3"}
		if _, _, _, err := f.Onboarding.SetOnboardingDefaults(ctx, cleared); err != nil {
			t.Fatalf("clear template: %v", err)
		}
		assertStoredOnboardingDefaults(t, f, cleared)
	})

	t.Run("account creation without a template or with an empty one enqueues no job", func(t *testing.T) {
		f := newFixture(t)
		createOnboardingAccount(t, f)
		if _, _, _, err := f.Onboarding.SetOnboardingDefaults(ctx, domain.OnboardingDefaults{CommandID: "empty"}); err != nil {
			t.Fatalf("set empty template: %v", err)
		}
		createOnboardingAccount(t, f)
		assertNoClaimableJobs(t, f)
	})

	t.Run("account creation enqueues a snapshot of the template at that moment", func(t *testing.T) {
		f := newFixture(t)
		lead, other := f.NewUser(t), f.NewUser(t)
		ch := f.NewChannel(t, lead.ID)
		tmpl := domain.OnboardingDefaults{
			Contacts:   []domain.OnboardingContact{{UserID: other.ID}, {UserID: lead.ID, Mutual: true}},
			ChannelIDs: []int64{ch},
		}
		setOnboardingTemplate(t, f, tmpl)
		created := createOnboardingAccount(t, f)
		// 之后修改模板不影响已写入的任务。
		setOnboardingTemplate(t, f, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: other.ID, Mutual: true}}})

		jobs := claimOnboardingJobs(t, f, 10, time.Minute)
		if len(jobs) != 1 {
			t.Fatalf("claimed %d jobs, want 1: %+v", len(jobs), jobs)
		}
		got := jobs[0]
		if got.UserID != created.ID || got.Attempts != 0 || got.LastError != "" || got.CreatedAt.IsZero() {
			t.Fatalf("job = %+v, want a fresh job for user %d", got, created.ID)
		}
		if !reflect.DeepEqual(got.Contacts, tmpl.Contacts) || !reflect.DeepEqual(got.ChannelIDs, tmpl.ChannelIDs) {
			t.Fatalf("job contacts=%+v channels=%v, want %+v %v", got.Contacts, got.ChannelIDs, tmpl.Contacts, tmpl.ChannelIDs)
		}
	})

	t.Run("failed account creation leaves no job", func(t *testing.T) {
		f := newFixture(t)
		lead := f.NewUser(t)
		setOnboardingTemplate(t, f, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: lead.ID}}})
		if _, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 2, Phone: lead.Phone, FirstName: "Dup"}, store.AccountExtras{}); !errors.Is(err, domain.ErrPhoneNumberOccupied) {
			t.Fatalf("create with an occupied phone err=%v, want ErrPhoneNumberOccupied", err)
		}
		assertNoClaimableJobs(t, f)
	})

	t.Run("claimed jobs are leased in creation order and complete removes them", func(t *testing.T) {
		f := newFixture(t)
		lead := f.NewUser(t)
		setOnboardingTemplate(t, f, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: lead.ID}}})
		first, second, third := createOnboardingAccount(t, f), createOnboardingAccount(t, f), createOnboardingAccount(t, f)

		batch := claimOnboardingJobs(t, f, 2, time.Minute)
		if ids := onboardingJobUserIDs(batch); !reflect.DeepEqual(ids, []int64{first.ID, second.ID}) {
			t.Fatalf("first claim = %v, want %v", ids, []int64{first.ID, second.ID})
		}
		// 租约内已领取的任务不会被再次领取。
		if ids := onboardingJobUserIDs(claimOnboardingJobs(t, f, 10, time.Minute)); !reflect.DeepEqual(ids, []int64{third.ID}) {
			t.Fatalf("second claim = %v, want only %d", ids, third.ID)
		}
		for _, id := range []int64{first.ID, second.ID, third.ID} {
			if err := f.Onboarding.CompleteOnboardingJob(ctx, id); err != nil {
				t.Fatalf("complete job %d: %v", id, err)
			}
		}
		if err := f.Onboarding.CompleteOnboardingJob(ctx, first.ID); err != nil {
			t.Fatalf("complete an already completed job: %v", err)
		}
		assertNoClaimableJobs(t, f)
	})

	t.Run("a job whose lease expired can be claimed again", func(t *testing.T) {
		f := newFixture(t)
		lead := f.NewUser(t)
		setOnboardingTemplate(t, f, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: lead.ID}}})
		created := createOnboardingAccount(t, f)
		if got := claimOnboardingJobs(t, f, 10, 50*time.Millisecond); len(got) != 1 {
			t.Fatalf("first claim got %d jobs, want 1", len(got))
		}
		time.Sleep(200 * time.Millisecond)
		if ids := onboardingJobUserIDs(claimOnboardingJobs(t, f, 10, time.Minute)); !reflect.DeepEqual(ids, []int64{created.ID}) {
			t.Fatalf("claim after lease expiry = %v, want %d", ids, created.ID)
		}
	})

	t.Run("retry records the failure and hides the job until retryAfter", func(t *testing.T) {
		f := newFixture(t)
		lead := f.NewUser(t)
		setOnboardingTemplate(t, f, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: lead.ID}}})
		created := createOnboardingAccount(t, f)
		claimOnboardingJobs(t, f, 10, time.Minute)
		if err := f.Onboarding.RetryOnboardingJob(ctx, created.ID, "boom", 150*time.Millisecond); err != nil {
			t.Fatalf("retry: %v", err)
		}
		assertNoClaimableJobs(t, f)
		time.Sleep(300 * time.Millisecond)
		jobs := claimOnboardingJobs(t, f, 10, time.Minute)
		if len(jobs) != 1 || jobs[0].UserID != created.ID || jobs[0].Attempts != 1 || jobs[0].LastError != "boom" {
			t.Fatalf("claim after retry = %+v, want the job with attempts=1 last_error=boom", jobs)
		}
	})

	t.Run("an abandoned job is never claimed again", func(t *testing.T) {
		f := newFixture(t)
		lead := f.NewUser(t)
		setOnboardingTemplate(t, f, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: lead.ID}}})
		created := createOnboardingAccount(t, f)
		claimOnboardingJobs(t, f, 10, 50*time.Millisecond)
		if err := f.Onboarding.AbandonOnboardingJob(ctx, created.ID, "gave up"); err != nil {
			t.Fatalf("abandon: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		assertNoClaimableJobs(t, f)
	})
}

func setOnboardingTemplate(t *testing.T, f OnboardingFixture, d domain.OnboardingDefaults) {
	t.Helper()
	if _, _, _, err := f.Onboarding.SetOnboardingDefaults(context.Background(), d); err != nil {
		t.Fatalf("set template: %v", err)
	}
}

func createOnboardingAccount(t *testing.T, f OnboardingFixture) domain.User {
	t.Helper()
	u, err := f.Accounts.CreateAccount(context.Background(), domain.User{AccessHash: 3, Phone: f.NewPhone(t), FirstName: "New"}, store.AccountExtras{})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return u
}

func claimOnboardingJobs(t *testing.T, f OnboardingFixture, limit int, lease time.Duration) []domain.OnboardingJob {
	t.Helper()
	jobs, err := f.Onboarding.ClaimOnboardingJobs(context.Background(), limit, lease)
	if err != nil {
		t.Fatalf("claim jobs: %v", err)
	}
	return jobs
}

func assertNoClaimableJobs(t *testing.T, f OnboardingFixture) {
	t.Helper()
	if jobs := claimOnboardingJobs(t, f, 10, time.Minute); len(jobs) != 0 {
		t.Fatalf("claimable jobs = %+v, want none", jobs)
	}
}

func onboardingJobUserIDs(jobs []domain.OnboardingJob) []int64 {
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.UserID
	}
	return ids
}

func assertStoredOnboardingDefaults(t *testing.T, f OnboardingFixture, want domain.OnboardingDefaults) {
	t.Helper()
	got, found, err := f.Onboarding.GetOnboardingDefaults(context.Background())
	if err != nil || !found {
		t.Fatalf("get template found=%v err=%v", found, err)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("stored template UpdatedAt is zero")
	}
	assertOnboardingDefaults(t, got, want)
}

// assertOnboardingDefaults 比较除 UpdatedAt 之外的全部字段；nil 与空名单等价。
func assertOnboardingDefaults(t *testing.T, got, want domain.OnboardingDefaults) {
	t.Helper()
	got, want = got.Clone(), want.Clone()
	got.UpdatedAt = want.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("template = %+v, want %+v", got, want)
	}
}
