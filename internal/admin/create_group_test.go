package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type fakeGroupCreator struct {
	calls     []fakeGroupCreateCall
	bypassed  bool
	channelID int64
}

type fakeGroupCreateCall struct {
	ownerID int64
	title   string
}

func (f *fakeGroupCreator) AdminCreateGroup(_ context.Context, ownerID int64, title string) (domain.AdminCreatedGroup, error) {
	f.calls = append(f.calls, fakeGroupCreateCall{ownerID: ownerID, title: title})
	return domain.AdminCreatedGroup{
		Channel:                   domain.Channel{ID: f.channelID, Title: title, Megagroup: true, CreatorUserID: ownerID},
		InviteLink:                "https://t.example.test/+abc",
		AccountPermissionBypassed: f.bypassed,
	}, nil
}

func newCreateGroupTestService(creator *fakeGroupCreator) *Service {
	frozenSince := fixedNow().Add(-time.Hour)
	return NewService(Dependencies{
		Commands: newMemoryCommandRepo(),
		Users: &fakeUsersService{users: map[int64]domain.User{
			1001:                        {ID: 1001, FirstName: "Company"},
			1002:                        {ID: 1002, FirstName: "Robot", Bot: true},
			1003:                        {ID: 1003, Deleted: true},
			1004:                        {ID: 1004, FirstName: "Frozen"},
			domain.OfficialSystemUserID: {ID: domain.OfficialSystemUserID, FirstName: "Telegram"},
		}},
		Restrictions: &fakeRestrictionStore{items: map[int64]domain.AccountFreeze{1004: {
			UserID: 1004, Frozen: true, Since: frozenSince, Until: fixedNow().Add(24 * time.Hour),
			AppealURL: "https://appeals.example.test/1004",
		}}},
		GroupCreator: creator,
		Now:          fixedNow,
	})
}

func TestCreateGroupDryRunRejectsUnusableOwnerOrTitle(t *testing.T) {
	cases := []struct {
		name    string
		ownerID int64
		title   string
		code    string
	}{
		{"missing owner", 1999, "Group", CodeGroupOwnerNotFound},
		{"bot owner", 1002, "Group", CodeGroupOwnerInvalid},
		{"system owner", domain.OfficialSystemUserID, "Group", CodeGroupOwnerInvalid},
		{"deleted owner", 1003, "Group", CodeGroupOwnerInvalid},
		{"frozen owner", 1004, "Group", CodeGroupOwnerFrozen},
		{"blank title", 1001, "   ", CodeGroupTitleInvalid},
		{"overlong title", 1001, strings.Repeat("群", domain.MaxChannelTitleLength+1), CodeGroupTitleInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creator := &fakeGroupCreator{channelID: 5001}
			svc := newCreateGroupTestService(creator)
			res, err := svc.CreateGroup(context.Background(), CreateGroupRequest{
				CommandMeta: CommandMeta{CommandID: "dry-create-group", Actor: "ops", Reason: "migration", DryRun: true},
				OwnerUserID: tc.ownerID,
				Title:       tc.title,
			})
			if err == nil || ErrorCode(err) != tc.code {
				t.Fatalf("err = %v (code %q), want code %s", err, ErrorCode(err), tc.code)
			}
			if res.Status != string(domain.AdminCommandFailed) || len(creator.calls) != 0 {
				t.Fatalf("result=%+v creator calls=%d, want failed dry-run without creating", res, len(creator.calls))
			}
		})
	}
}

func TestCreateGroupDryRunValidatesWithoutCreating(t *testing.T) {
	creator := &fakeGroupCreator{channelID: 5001}
	svc := newCreateGroupTestService(creator)
	res, err := svc.CreateGroup(context.Background(), CreateGroupRequest{
		CommandMeta: CommandMeta{CommandID: "dry-create-group", Actor: "ops", Reason: "migration", DryRun: true},
		OwnerUserID: 1001,
		Title:       "  Sales Team  ",
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !res.DryRun || res.Status != string(domain.AdminCommandCompleted) || res.TargetUserID != 1001 {
		t.Fatalf("dry-run result = %+v", res)
	}
	if res.Details["title"] != "Sales Team" || len(creator.calls) != 0 {
		t.Fatalf("details=%+v creator calls=%d, want trimmed title and no creation", res.Details, len(creator.calls))
	}
}

func TestCreateGroupExecutesOnceAndReplaysStoredResult(t *testing.T) {
	ctx := context.Background()
	creator := &fakeGroupCreator{channelID: 5001, bypassed: true}
	svc := newCreateGroupTestService(creator)
	req := CreateGroupRequest{
		CommandMeta: CommandMeta{CommandID: "exec-create-group", Actor: "ops", Reason: "migration"},
		OwnerUserID: 1001,
		Title:       "  Sales Team  ",
	}
	res, err := svc.CreateGroup(ctx, req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(creator.calls) != 1 || creator.calls[0] != (fakeGroupCreateCall{ownerID: 1001, title: "Sales Team"}) {
		t.Fatalf("creator calls = %+v, want one call with trimmed title", creator.calls)
	}
	if res.TargetUserID != 1001 || res.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("result = %+v", res)
	}
	want := map[string]any{
		"owner_user_id":               int64(1001),
		"title":                       "Sales Team",
		"channel_id":                  int64(5001),
		"invite_link":                 "https://t.example.test/+abc",
		"account_permission_bypassed": true,
	}
	for key, value := range want {
		if res.Details[key] != value {
			t.Fatalf("details[%s] = %#v, want %#v (details %+v)", key, res.Details[key], value, res.Details)
		}
	}

	replay, err := svc.CreateGroup(ctx, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.AlreadyExecuted || len(creator.calls) != 1 {
		t.Fatalf("replay=%+v creator calls=%d, want stored result without a second group", replay, len(creator.calls))
	}
	if replay.Details["invite_link"] != "https://t.example.test/+abc" {
		t.Fatalf("replay details = %+v, want stored invite link", replay.Details)
	}
}
