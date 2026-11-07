package onboarding_test

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// 入驻时在会话里写"XX 加入了 …"（messageActionContactSignUp），让默认联系人出现在聊天列表里。

func TestExecuteGivesTheNewAccountAChatWithEachDefaultContact(t *testing.T) {
	f := newFixture(t)
	support, newcomer := f.newUser(t, "Support"), f.newUser(t, "Newcomer")

	if _, err := f.svc.Execute(context.Background(), job(newcomer.ID, []domain.OnboardingContact{{UserID: support.ID}})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	dialog, msg, found := f.signUpNotice(t, newcomer.ID, support.ID)
	if !found || !isContactSignUpFrom(msg, support.ID) || dialog.UnreadCount != 1 || msg.Silent {
		t.Fatalf("newcomer chat with support = %+v top=%+v found=%v, want an unread sign-up notice from support", dialog, msg, found)
	}
	// 单向联系人：对方那边什么都不出现。
	if _, _, found := f.signUpNotice(t, support.ID, newcomer.ID); found {
		t.Fatal("a one-way default contact got a chat with the newcomer")
	}
}

func TestExecuteWritesNoSignUpNoticeForABotContact(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bot, err := f.users.Create(ctx, domain.User{AccessHash: 1, FirstName: "HelperBot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	newcomer := f.newUser(t, "Newcomer")

	if _, err := f.svc.Execute(ctx, job(newcomer.ID, []domain.OnboardingContact{{UserID: bot.ID}})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, _, found := f.signUpNotice(t, newcomer.ID, bot.ID); found {
		t.Fatal("the newcomer got a chat with a bot default contact")
	}
	if got := f.contactIDs(t, newcomer.ID); len(got) != 1 || got[0] != bot.ID {
		t.Fatalf("newcomer contacts = %v, want the bot", got)
	}
}

func TestExecuteTellsAMutualContactThatTheNewAccountJoined(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	loud, quiet, newcomer := f.newUser(t, "Loud"), f.newUser(t, "Quiet"), f.newUser(t, "Newcomer")
	if _, err := f.account.SetContactSignUpSilent(ctx, quiet.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.Execute(ctx, job(newcomer.ID, []domain.OnboardingContact{{UserID: loud.ID, Mutual: true}, {UserID: quiet.ID, Mutual: true}})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, c := range []struct {
		name   string
		id     int64
		silent bool
	}{{"loud", loud.ID, false}, {"quiet", quiet.ID, true}} {
		dialog, msg, found := f.signUpNotice(t, c.id, newcomer.ID)
		if !found || !isContactSignUpFrom(msg, newcomer.ID) || dialog.UnreadCount != 1 || msg.Silent != c.silent {
			t.Fatalf("%s contact's chat with the newcomer = %+v top=%+v found=%v, want a sign-up notice from the newcomer silent=%v", c.name, dialog, msg, found, c.silent)
		}
	}
}

func TestExecuteRunTwiceWritesEachSignUpNoticeOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	support, newcomer := f.newUser(t, "Support"), f.newUser(t, "Newcomer")
	j := job(newcomer.ID, []domain.OnboardingContact{{UserID: support.ID, Mutual: true}})
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Execute(ctx, j); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}
	for _, side := range [][2]int64{{newcomer.ID, support.ID}, {support.ID, newcomer.ID}} {
		if dialog, _, _ := f.signUpNotice(t, side[0], side[1]); dialog.UnreadCount != 1 {
			t.Fatalf("unread of %d with %d after a rerun = %d, want 1", side[0], side[1], dialog.UnreadCount)
		}
	}
}
