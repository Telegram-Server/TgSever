package bots

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"testing"
)

func TestManagedBotCredentialsAndAccess(t *testing.T) {
	ctx := context.Background()
	svc, users, botStore, _ := newTestService(t)
	owner := newOwner(t, users, "+20441")
	allowed := newOwner(t, users, "+20442")
	stranger := newOwner(t, users, "+20443")
	manager := makeBot(t, svc, owner, "Manager", "managed_authority_bot")
	other := makeBot(t, svc, owner, "Other", "other_authority_bot")
	if _, _, err := svc.CreateManagedBot(ctx, owner.ID, manager.ID, "Child", "managed_child_bot"); !errors.Is(err, domain.ErrBotManagerInvalid) {
		t.Fatalf("disabled manager create=%v", err)
	}
	if err := svc.SetCanManageBots(ctx, stranger.ID, manager.ID, true); err == nil {
		t.Fatal("stranger enabled manager")
	}
	if err := svc.SetCanManageBots(ctx, owner.ID, manager.ID, true); err != nil {
		t.Fatal(err)
	}
	child, initial, err := svc.CreateManagedBot(ctx, owner.ID, manager.ID, "Child", "managed_child_bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ManagedBotToken(ctx, other.ID, child.ID, false); !errors.Is(err, domain.ErrBotManagerInvalid) {
		t.Fatalf("unrelated manager got token=%v", err)
	}
	token, err := svc.ManagedBotToken(ctx, manager.ID, child.ID, false)
	if err != nil || token != initial {
		t.Fatalf("managed token=%q %v", token, err)
	}
	revoker := &captureRevoker{}
	svc.SetRouterHooks(revoker)
	rotated, err := svc.ManagedBotToken(ctx, manager.ID, child.ID, true)
	if err != nil || rotated == initial {
		t.Fatalf("rotation=%q %v", rotated, err)
	}
	profile, found, err := botStore.GetBot(ctx, child.ID)
	_, oldSecret, _ := domain.ParseBotToken(initial)
	_, newSecret, _ := domain.ParseBotToken(rotated)
	if err != nil || !found || profile.TokenSecret == oldSecret || profile.TokenSecret != newSecret || revoker.botUserID != child.ID {
		t.Fatalf("old credentials/session survived: profile=%+v revoked=%d err=%v", profile, revoker.botUserID, err)
	}
	if err := svc.SetBotAccessSettings(ctx, other.ID, child.ID, true, []int64{allowed.ID}); err == nil {
		t.Fatal("unrelated manager set access")
	}
	if err := svc.SetBotAccessSettings(ctx, manager.ID, child.ID, true, []int64{allowed.ID}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		id   int64
		want bool
	}{{owner.ID, true}, {allowed.ID, true}, {stranger.ID, false}, {manager.ID, false}} {
		got, err := svc.BotAccessAllowed(ctx, child.ID, step.id)
		if err != nil || got != step.want {
			t.Fatalf("access user=%d got=%v,%v want=%v", step.id, got, err, step.want)
		}
	}
	if err := svc.SetBotAccessSettings(ctx, owner.ID, child.ID, false, []int64{-1}); err != nil {
		t.Fatalf("unrestricted ignored list=%v", err)
	}
	if got, err := svc.BotAccessAllowed(ctx, child.ID, stranger.ID); err != nil || !got {
		t.Fatalf("cleared restriction=%v,%v", got, err)
	}
}

func TestManagedRotationFailsClosedWithoutSessionRevocation(t *testing.T) {
	ctx := context.Background()
	svc, users, bots, _ := newTestService(t)
	owner := newOwner(t, users, "+20444")
	manager := makeBot(t, svc, owner, "Manager", "managed_revoke_guard_bot")
	if err := svc.SetCanManageBots(ctx, owner.ID, manager.ID, true); err != nil {
		t.Fatal(err)
	}
	child, initial, err := svc.CreateManagedBot(ctx, owner.ID, manager.ID, "Child", "guarded_child_bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ManagedBotToken(ctx, manager.ID, child.ID, true); !errors.Is(err, domain.ErrBotSessionsNotRevoked) {
		t.Fatalf("rotation without revoker=%v", err)
	}
	p, _, err := bots.GetBot(ctx, child.ID)
	if err != nil || domain.FormatBotToken(child.ID, p.TokenSecret) != initial {
		t.Fatalf("failed precondition changed token=%+v %v", p, err)
	}
}
