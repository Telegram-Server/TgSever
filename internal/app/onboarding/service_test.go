package onboarding_test

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/app/channels"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/onboarding"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type fixture struct {
	svc      *onboarding.Service
	users    *memory.UserStore
	contacts *contacts.Service
	channels *memory.ChannelStore
	store    *memory.OnboardingStore
	phones   atomic.Int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		users:    memory.NewUserStore(),
		channels: memory.NewChannelStore(),
		store:    memory.NewOnboardingStore(),
	}
	f.contacts = contacts.NewService(memory.NewContactStore(), f.users)
	f.svc = onboarding.NewService(f.store, f.users, f.contacts, channels.NewService(f.channels))
	return f
}

func (f *fixture) nextPhone() string {
	return fmt.Sprintf("1555044%04d", f.phones.Add(1))
}

// memoryAccounts 是与 fixture 共用账号和入驻 store 的建号操作。
func memoryAccounts(f *fixture) *memory.AccountCreationStore {
	return memory.NewAccountCreationStore(f.users, nil, nil).WithOnboarding(f.store)
}

func (f *fixture) newUser(t *testing.T, firstName string) domain.User {
	t.Helper()
	u, err := f.users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: firstName, Phone: f.nextPhone()})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// contactIDs 返回 owner 通讯录里的账号 id（升序）。
func (f *fixture) contactIDs(t *testing.T, owner int64) []int64 {
	t.Helper()
	list, _, err := f.contacts.GetContacts(context.Background(), owner, 0)
	if err != nil {
		t.Fatalf("get contacts of %d: %v", owner, err)
	}
	ids := make([]int64, 0, len(list.Contacts))
	for _, c := range list.Contacts {
		ids = append(ids, c.User.ID)
	}
	slices.Sort(ids)
	return ids
}

func (f *fixture) newGroup(t *testing.T, owner int64, members ...int64) int64 {
	t.Helper()
	res, err := f.channels.CreateChannel(context.Background(), domain.CreateChannelRequest{CreatorUserID: owner, Title: "Lobby", Megagroup: true, MemberUserIDs: members, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	return res.Channel.ID
}

func (f *fixture) isMember(t *testing.T, owner, channelID, userID int64) bool {
	t.Helper()
	m, err := f.channels.GetParticipant(context.Background(), owner, channelID, userID)
	return err == nil && m.Status == domain.ChannelMemberActive
}

func (f *fixture) deleteAccount(t *testing.T, userID int64) {
	t.Helper()
	if _, err := memory.NewAccountLifecycleStore(f.users, nil).ExecuteAccountDeletion(context.Background(), userID, domain.AccountDeletionManual, "onboarding test", time.Now()); err != nil {
		t.Fatalf("delete account %d: %v", userID, err)
	}
}

func job(userID int64, contacts []domain.OnboardingContact, channelIDs ...int64) domain.OnboardingJob {
	return domain.OnboardingJob{UserID: userID, Contacts: contacts, ChannelIDs: channelIDs, CreatedAt: time.Now()}
}

func TestExecuteAddsOneWayContactOnlyToTheNewAccount(t *testing.T) {
	f := newFixture(t)
	support, newcomer := f.newUser(t, "Support"), f.newUser(t, "Newcomer")

	if _, err := f.svc.Execute(context.Background(), job(newcomer.ID, []domain.OnboardingContact{{UserID: support.ID}})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := f.contactIDs(t, newcomer.ID); !slices.Equal(got, []int64{support.ID}) {
		t.Fatalf("newcomer contacts = %v, want [%d]", got, support.ID)
	}
	if got := f.contactIDs(t, support.ID); len(got) != 0 {
		t.Fatalf("support contacts = %v, want none for a one-way default contact", got)
	}
}

func TestExecuteAddsMutualContactToBothSides(t *testing.T) {
	f := newFixture(t)
	support, newcomer := f.newUser(t, "Support"), f.newUser(t, "Newcomer")

	res, err := f.svc.Execute(context.Background(), job(newcomer.ID, []domain.OnboardingContact{{UserID: support.ID, Mutual: true}}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := f.contactIDs(t, newcomer.ID); !slices.Equal(got, []int64{support.ID}) {
		t.Fatalf("newcomer contacts = %v, want [%d]", got, support.ID)
	}
	if got := f.contactIDs(t, support.ID); !slices.Equal(got, []int64{newcomer.ID}) {
		t.Fatalf("support contacts = %v, want [%d]", got, newcomer.ID)
	}
	owners := []int64{}
	for _, c := range res.Contacts {
		owners = append(owners, c.OwnerUserID)
	}
	if !slices.Equal(owners, []int64{newcomer.ID, support.ID}) {
		t.Fatalf("result contact owners = %v, want newcomer then support", owners)
	}
}

func TestExecuteSkipsDeletedDefaultContactsAndKeepsTheRest(t *testing.T) {
	f := newFixture(t)
	gone, kept, newcomer := f.newUser(t, "Gone"), f.newUser(t, "Kept"), f.newUser(t, "Newcomer")
	f.deleteAccount(t, gone.ID)

	res, err := f.svc.Execute(context.Background(), job(newcomer.ID, []domain.OnboardingContact{{UserID: gone.ID, Mutual: true}, {UserID: kept.ID}}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := f.contactIDs(t, newcomer.ID); !slices.Equal(got, []int64{kept.ID}) {
		t.Fatalf("newcomer contacts = %v, want only [%d]", got, kept.ID)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Kind != onboarding.SkippedContact || res.Skipped[0].ID != gone.ID {
		t.Fatalf("skipped = %+v, want the deleted contact", res.Skipped)
	}
}

func TestExecuteJoinsAGroupThatRequiresApprovalDirectly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, newcomer := f.newUser(t, "Owner"), f.newUser(t, "Newcomer")
	group := f.newGroup(t, owner.ID)
	if _, err := f.channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{UserID: owner.ID, ChannelID: group, Username: "lobbygroup"}); err != nil {
		t.Fatalf("set username: %v", err)
	}
	if _, err := f.channels.SetJoinRequest(ctx, owner.ID, group, true); err != nil {
		t.Fatalf("enable join requests: %v", err)
	}

	res, err := f.svc.Execute(ctx, job(newcomer.ID, nil, group))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !f.isMember(t, owner.ID, group, newcomer.ID) {
		t.Fatal("newcomer is not an active member of the approval-required group")
	}
	if len(res.Joins) != 1 || res.Joins[0].Channel.ID != group {
		t.Fatalf("joins = %+v, want the group", res.Joins)
	}
}

func TestExecuteSkipsChannelsTheNewAccountCannotJoinAndKeepsTheRest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, newcomer := f.newUser(t, "Owner"), f.newUser(t, "Newcomer")
	banned := f.newGroup(t, owner.ID, newcomer.ID)
	if _, err := f.channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID: owner.ID, ChannelID: banned, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: newcomer.ID},
		BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: int(time.Now().Unix()),
	}); err != nil {
		t.Fatalf("ban newcomer: %v", err)
	}
	const missing = int64(1) << 50
	open := f.newGroup(t, owner.ID)

	res, err := f.svc.Execute(ctx, job(newcomer.ID, nil, banned, missing, open))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !f.isMember(t, owner.ID, open, newcomer.ID) {
		t.Fatal("newcomer did not join the open group after the skipped ones")
	}
	skipped := []int64{}
	for _, s := range res.Skipped {
		if s.Kind == onboarding.SkippedChannel {
			skipped = append(skipped, s.ID)
		}
	}
	if !slices.Equal(skipped, []int64{banned, missing}) {
		t.Fatalf("skipped channels = %v, want [%d %d]", skipped, banned, missing)
	}
}

func TestExecuteRunTwiceAddsNothingTwice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, support, newcomer := f.newUser(t, "Owner"), f.newUser(t, "Support"), f.newUser(t, "Newcomer")
	group := f.newGroup(t, owner.ID)
	j := job(newcomer.ID, []domain.OnboardingContact{{UserID: support.ID, Mutual: true}}, group)
	if _, err := f.svc.Execute(ctx, j); err != nil {
		t.Fatalf("first execute: %v", err)
	}

	again, err := f.svc.Execute(ctx, j)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if len(again.Joins) != 0 {
		t.Fatalf("second run joins = %+v, want none: the newcomer is already a member", again.Joins)
	}
	if got := f.contactIDs(t, newcomer.ID); !slices.Equal(got, []int64{support.ID}) {
		t.Fatalf("newcomer contacts after rerun = %v, want [%d]", got, support.ID)
	}
	if got := f.contactIDs(t, support.ID); !slices.Equal(got, []int64{newcomer.ID}) {
		t.Fatalf("support contacts after rerun = %v, want [%d]", got, newcomer.ID)
	}
}

func TestExecuteDoesNothingForADeletedNewAccount(t *testing.T) {
	f := newFixture(t)
	support, newcomer := f.newUser(t, "Support"), f.newUser(t, "Newcomer")
	f.deleteAccount(t, newcomer.ID)

	if _, err := f.svc.Execute(context.Background(), job(newcomer.ID, []domain.OnboardingContact{{UserID: support.ID, Mutual: true}})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := f.contactIDs(t, support.ID); len(got) != 0 {
		t.Fatalf("support contacts = %v, want none for a deleted new account", got)
	}
}
