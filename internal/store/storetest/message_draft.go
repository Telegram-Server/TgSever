package storetest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
	"time"
)

// MessageDraftLifecycle runs unchanged for the memory and Redis transient stores.
func MessageDraftLifecycle(t *testing.T, s store.MessageDraftStore, seed int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	draft := domain.MessageDraft{BotUserID: seed + 1, UserID: seed + 2, ID: 1<<60 + 7, CanStop: true, Action: []byte("first"), ExpiresAt: now.Add(domain.MessageDraftLifetime)}
	replace := func(d domain.MessageDraft, at time.Time) {
		t.Helper()
		if err := s.ReplaceMessageDraft(ctx, d, at); err != nil {
			t.Fatal(err)
		}
	}
	stop := func(d domain.MessageDraft, at time.Time, want error) domain.MessageDraft {
		t.Helper()
		got, changed, err := s.StopMessageDraft(ctx, d.BotUserID, d.UserID, d.ThreadID, d.ID, at)
		if !errors.Is(err, want) || (want == nil && !changed) {
			t.Fatalf("stop changed=%v err=%v want=%v", changed, err, want)
		}
		return got
	}
	replace(draft, now)
	// Latest partial content replaces rather than creates a second preview.
	draft.Action = []byte("latest")
	draft.KeepOnStop = true
	replace(draft, now)
	otherUser := draft
	otherUser.UserID++
	otherBot := draft
	otherBot.BotUserID += 10
	otherThread := draft
	otherThread.ThreadID = 17
	for _, d := range []domain.MessageDraft{otherUser, otherBot, otherThread} {
		replace(d, now)
	}
	stopped := stop(draft, now, nil)
	if string(stopped.Action) != "latest" || !stopped.KeepOnStop {
		t.Fatalf("stop did not preserve latest preview: %+v", stopped)
	}
	if err := s.ReplaceMessageDraft(ctx, draft, now); !errors.Is(err, domain.ErrMessageDraftStopped) {
		t.Fatalf("update after stop: %v", err)
	}
	if _, changed, err := s.StopMessageDraft(ctx, draft.BotUserID, draft.UserID, 0, draft.ID, now); err != nil || changed {
		t.Fatalf("duplicate stop changed=%v err=%v", changed, err)
	}
	for _, d := range []domain.MessageDraft{otherUser, otherBot, otherThread} {
		stop(d, now, nil)
	}

	replacement := draft
	replacement.ID++
	replace(replacement, now)
	newer := replacement
	newer.ID++
	newer.CanStop = false
	replace(newer, now)
	stop(replacement, now, domain.ErrMessageDraftExpired)
	stop(newer, now, domain.ErrMessageDraftStopForbidden)
	newer.CanStop = true
	replace(newer, now)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := s.StopMessageDraft(ctx, newer.BotUserID, newer.UserID, 0, newer.ID, now)
			if err != nil {
				t.Errorf("concurrent stop: %v", err)
			}
			if changed {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d concurrent stop actions", accepted.Load())
	}

	expired := draft
	expired.ID += 20
	replace(expired, now)
	future := now.Add(domain.MessageDraftLifetime)
	stop(expired, future, domain.ErrMessageDraftExpired)
	expired.ExpiresAt = future.Add(domain.MessageDraftLifetime)
	replace(expired, future)
	if err := s.FinishMessageDraft(ctx, expired.BotUserID, expired.UserID, 0, future); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceMessageDraft(ctx, expired, future); !errors.Is(err, domain.ErrMessageDraftStopped) {
		t.Fatalf("finalized draft resurrected: %v", err)
	}
	stop(expired, future, domain.ErrMessageDraftExpired)
}
