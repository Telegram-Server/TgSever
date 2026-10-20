package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestLoginPasswordChallengeStoreTakesOnceAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s := NewLoginPasswordChallengeStore(func() time.Time { return now })

	first := store.LoginPasswordChallenge{SRPID: 7, UserID: 1, Phone: "1", B: []byte{1}, ExpiresAt: now.Add(time.Minute)}
	if err := s.SaveLoginPasswordChallenge(ctx, first); err != nil {
		t.Fatalf("save: %v", err)
	}
	clash := first
	clash.UserID = 2
	if err := s.SaveLoginPasswordChallenge(ctx, clash); !errors.Is(err, store.ErrLoginPasswordChallengeExists) {
		t.Fatalf("save over a pending challenge = %v, want ErrLoginPasswordChallengeExists", err)
	}
	if err := s.SaveLoginPasswordChallenge(ctx, store.LoginPasswordChallenge{}); !errors.Is(err, store.ErrLoginPasswordChallengeExists) {
		t.Fatalf("save with srp_id 0 = %v, want ErrLoginPasswordChallengeExists", err)
	}

	got, found, err := s.TakeLoginPasswordChallenge(ctx, 7)
	if err != nil || !found || got.UserID != 1 {
		t.Fatalf("take = %+v, %v, %v; want the first challenge", got, found, err)
	}
	if _, found, _ := s.TakeLoginPasswordChallenge(ctx, 7); found {
		t.Fatal("second take found the challenge again")
	}
}

func TestLoginPasswordChallengeStoreSweepsExpiredChallenges(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s := NewLoginPasswordChallengeStore(func() time.Time { return now })
	if err := s.SaveLoginPasswordChallenge(ctx, store.LoginPasswordChallenge{SRPID: 1, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := s.SaveLoginPasswordChallenge(ctx, store.LoginPasswordChallenge{SRPID: 2, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, stale := s.m[1]
	_, fresh := s.m[2]
	s.mu.Unlock()
	if stale || !fresh {
		t.Fatalf("after sweep: expired present=%v, fresh present=%v; want false, true", stale, fresh)
	}
}
