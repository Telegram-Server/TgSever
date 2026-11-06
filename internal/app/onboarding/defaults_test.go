package onboarding_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

func TestSetDefaultsWritesAValidTemplate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, support := f.newUser(t, "Owner"), f.newUser(t, "Support")
	group := f.newGroup(t, owner.ID)
	want := domain.OnboardingDefaults{
		Contacts:   []domain.OnboardingContact{{UserID: support.ID, Mutual: true}, {UserID: owner.ID}},
		ChannelIDs: []int64{group},
	}

	_, found, written, err := f.svc.SetDefaults(ctx, want, "cmd-1")
	if err != nil || found {
		t.Fatalf("set defaults found=%v err=%v, want first write", found, err)
	}
	if written.CommandID != "cmd-1" || !reflect.DeepEqual(written.Contacts, want.Contacts) || !reflect.DeepEqual(written.ChannelIDs, want.ChannelIDs) {
		t.Fatalf("written = %+v, want %+v with command id cmd-1", written, want)
	}
	got, found, err := f.svc.Defaults(ctx)
	if err != nil || !found || !reflect.DeepEqual(got.Contacts, want.Contacts) || !reflect.DeepEqual(got.ChannelIDs, want.ChannelIDs) {
		t.Fatalf("defaults = %+v found=%v err=%v, want %+v", got, found, err, want)
	}
}

func TestSetDefaultsRejectsAnInvalidTemplateWithoutWriting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, support, gone := f.newUser(t, "Owner"), f.newUser(t, "Support"), f.newUser(t, "Gone")
	f.deleteAccount(t, gone.ID)
	bot, err := f.users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Helper", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	group := f.newGroup(t, owner.ID)
	tooMany := make([]domain.OnboardingContact, domain.MaxOnboardingContacts+1)
	for i := range tooMany {
		tooMany[i] = domain.OnboardingContact{UserID: support.ID + int64(i)}
	}
	const missing = int64(1) << 50

	cases := map[string]domain.OnboardingDefaults{
		"too many contacts":    {Contacts: tooMany},
		"too many channels":    {ChannelIDs: make([]int64, domain.MaxOnboardingChannels+1)},
		"duplicate contact":    {Contacts: []domain.OnboardingContact{{UserID: support.ID}, {UserID: support.ID, Mutual: true}}},
		"duplicate channel":    {ChannelIDs: []int64{group, group}},
		"missing contact":      {Contacts: []domain.OnboardingContact{{UserID: missing}}},
		"deleted contact":      {Contacts: []domain.OnboardingContact{{UserID: gone.ID}}},
		"mutual bot contact":   {Contacts: []domain.OnboardingContact{{UserID: bot.ID, Mutual: true}}},
		"missing channel":      {ChannelIDs: []int64{missing}},
		"zero contact user id": {Contacts: []domain.OnboardingContact{{UserID: 0}}},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := f.svc.SetDefaults(ctx, d, "cmd-bad"); !errors.Is(err, domain.ErrOnboardingDefaultsInvalid) {
				t.Fatalf("set defaults err=%v, want ErrOnboardingDefaultsInvalid", err)
			}
			if _, found, err := f.svc.Defaults(ctx); err != nil || found {
				t.Fatalf("template found=%v err=%v after a rejected write, want none", found, err)
			}
		})
	}

	// 单向的 bot 联系人是允许的。
	if _, _, _, err := f.svc.SetDefaults(ctx, domain.OnboardingDefaults{Contacts: []domain.OnboardingContact{{UserID: bot.ID}}}, "cmd-bot"); err != nil {
		t.Fatalf("one-way bot contact: %v", err)
	}
}
