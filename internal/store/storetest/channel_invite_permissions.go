package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func ChannelInvitePermissions(t *testing.T, s store.ChannelStore, users [5]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, b, editor, requester := users[0], users[1], users[2], users[3], users[4]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Invite permission", Megagroup: true, MemberUserIDs: []int64{a, b, editor}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	for _, admin := range []struct {
		id     int64
		rights domain.ChannelAdminRights
	}{{a, domain.ChannelAdminRights{InviteUsers: true}}, {b, domain.ChannelAdminRights{InviteUsers: true}}, {editor, domain.ChannelAdminRights{ChangeInfo: true}}} {
		if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: admin.id, AdminRights: admin.rights, Date: baseNow()}); err != nil {
			t.Fatal(err)
		}
	}
	link, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: a, ChannelID: id, Title: "Approval", RequestNeeded: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("pending notifications exclude change-info-only admin", func(t *testing.T) {
		admins, err := s.ListChannelInviteAdminMemberIDs(ctx, id, 100)
		if err != nil {
			t.Fatal(err)
		}
		expected := map[int64]bool{owner: false, a: false, b: false}
		for _, userID := range admins {
			if _, ok := expected[userID]; !ok {
				t.Fatalf("unauthorized pending-update recipient %d", userID)
			}
			expected[userID] = true
		}
		for userID, found := range expected {
			if !found {
				t.Fatalf("missing invited administrator %d", userID)
			}
		}
	})
	t.Run("other admin cannot manage link", func(t *testing.T) {
		checks := []struct {
			name string
			run  func() error
		}{
			{"query", func() error {
				_, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: b, ChannelID: id, Hash: link.Invite.Hash})
				return err
			}},
			{"list", func() error {
				_, err := s.ListExportedInvites(ctx, domain.ChannelInviteListRequest{UserID: b, ChannelID: id, AdminUserID: a, Limit: 10})
				return err
			}},
			{"edit", func() error {
				_, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: b, ChannelID: id, Hash: link.Invite.Hash, HasTitle: true, Title: "Stolen", Date: baseNow()})
				return err
			}},
			{"revoke", func() error {
				_, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: b, ChannelID: id, Hash: link.Invite.Hash, Revoked: true, Date: baseNow()})
				return err
			}},
			{"delete", func() error {
				return s.DeleteExportedInvite(ctx, domain.DeleteChannelInviteRequest{UserID: b, ChannelID: id, Hash: link.Invite.Hash})
			}},
			{"delete revoked", func() error {
				return s.DeleteRevokedExportedInvites(ctx, domain.DeleteRevokedChannelInvitesRequest{UserID: b, ChannelID: id, AdminUserID: a})
			}},
			{"batch approve by other link", func() error {
				_, err := s.HideAllChatJoinRequests(ctx, domain.HideChannelJoinRequestsRequest{UserID: b, ChannelID: id, Hash: link.Invite.Hash, Approved: true, Date: baseNow()})
				return err
			}},
		}
		for _, check := range checks {
			t.Run(check.name, func(t *testing.T) {
				if err := check.run(); !errors.Is(err, domain.ErrChannelAdminRequired) {
					t.Fatalf("error=%v, want admin required", err)
				}
			})
		}
		current, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Invite.Hash})
		if err != nil || current.Title != "Approval" || current.Revoked {
			t.Fatalf("unauthorized operation changed link: %+v err=%v", current, err)
		}
	})
	t.Run("change info is not invite permission", func(t *testing.T) {
		if _, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: editor, ChannelID: id, Date: baseNow()}); !errors.Is(err, domain.ErrChannelAdminRequired) {
			t.Fatalf("export err=%v", err)
		}
		if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: requester, Hash: link.Invite.Hash, Date: baseNow()}); !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatal(err)
		}
		if _, err := s.ListInviteImporters(ctx, domain.ChannelInviteImportersRequest{UserID: editor, ChannelID: id, Requested: true}); !errors.Is(err, domain.ErrChannelAdminRequired) {
			t.Fatalf("list requests err=%v", err)
		}
		if _, err := s.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: editor, ChannelID: id, TargetUserID: requester, Approved: true, Date: baseNow()}); !errors.Is(err, domain.ErrChannelAdminRequired) {
			t.Fatalf("approve err=%v", err)
		}
	})
	t.Run("owner and originating admin can edit", func(t *testing.T) {
		for _, actor := range []int64{a, owner} {
			if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: actor, ChannelID: id, Hash: link.Invite.Hash, HasTitle: true, Title: "Allowed", Date: baseNow()}); err != nil {
				t.Fatalf("actor=%d err=%v", actor, err)
			}
		}
	})
	t.Run("revoked invite cannot be edited", func(t *testing.T) {
		extra, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: a, ChannelID: id, Title: "Revoked", Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: a, ChannelID: id, Hash: extra.Invite.Hash, Revoked: true, Date: baseNow()}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: a, ChannelID: id, Hash: extra.Invite.Hash, HasTitle: true, Title: "Changed", Date: baseNow()}); !errors.Is(err, domain.ErrInviteRevokedMissing) {
			t.Fatalf("revoked edit error=%v", err)
		}
		actual, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: extra.Invite.Hash})
		if err != nil || !actual.Revoked || actual.Title != "Revoked" {
			t.Fatalf("revoked link changed: %+v, %v", actual, err)
		}
	})
	t.Run("omitted link cannot expose another admin importers", func(t *testing.T) {
		group, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Importer ownership", Megagroup: true, MemberUserIDs: []int64{a, b}, Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		for _, admin := range []int64{a, b} {
			if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: group.Channel.ID, MemberID: admin, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: baseNow()}); err != nil {
				t.Fatal(err)
			}
		}
		invite, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: a, ChannelID: group.Channel.ID, Title: "Owned", Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: requester, Hash: invite.Invite.Hash, Date: baseNow()}); err != nil {
			t.Fatal(err)
		}
		for _, viewer := range []int64{owner, a, b} {
			list, err := s.ListInviteImporters(ctx, domain.ChannelInviteImportersRequest{UserID: viewer, ChannelID: group.Channel.ID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if viewer == b {
				if list.Count != 0 || len(list.Importers) != 0 {
					t.Fatalf("other admin saw importers: %+v", list)
				}
			} else if list.Count != 1 || len(list.Importers) != 1 || list.Importers[0].UserID != requester {
				t.Fatalf("authorized importer list=%+v", list)
			}
		}
	})
}

func ChannelInviteParameterValidation(t *testing.T, s store.ChannelStore, owner int64) {
	t.Helper()
	ctx := context.Background()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Invite parameters", Megagroup: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	for _, tc := range []struct {
		name          string
		expire, limit int
		approval      bool
		want          error
	}{
		{"past expiry", baseNow() - 1, 0, false, domain.ErrInviteExpireDateInvalid},
		{"negative limit", 0, -1, false, domain.ErrInviteUsageLimitInvalid},
		{"too many members", 0, 100000, false, domain.ErrInviteUsageLimitInvalid},
		{"approval and limit", 0, 1, true, domain.ErrInviteUsageLimitInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, ExpireDate: tc.expire, UsageLimit: tc.limit, RequestNeeded: tc.approval, Date: baseNow()})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
	title := strings.Repeat("链", domain.MaxChannelInviteTitleLength)
	link, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, Title: title, RequestNeeded: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	if link.Invite.Title != title {
		t.Fatal("Unicode title was changed")
	}
	if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Invite.Hash, HasUsageLimit: true, UsageLimit: 1, Date: baseNow()}); !errors.Is(err, domain.ErrInviteUsageLimitInvalid) {
		t.Fatalf("edit approval/limit error=%v", err)
	}
	if _, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Invite.Hash, HasExpireDate: true, ExpireDate: baseNow() - 1, Date: baseNow()}); !errors.Is(err, domain.ErrInviteExpireDateInvalid) {
		t.Fatalf("edit expiry error=%v", err)
	}
	actual, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: link.Invite.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if actual.UsageLimit != 0 || actual.ExpireDate != 0 || actual.Title != title || !actual.RequestNeeded {
		t.Fatalf("failed edits changed invite: %+v", actual)
	}
}
