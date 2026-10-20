package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func ChannelInviteIdentity(t *testing.T, s store.ChannelStore, owner int64) {
	t.Helper()
	ctx := context.Background()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Invite identity", Megagroup: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	main, err := s.EnsurePermanentInvite(ctx, id, owner, baseNow())
	if err != nil {
		t.Fatal(err)
	}
	additional, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	if additional.Invite.Permanent {
		t.Fatal("parameterless export must be additional")
	}
	edited, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: additional.Invite.Hash, HasTitle: true, Title: "Campaign", Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	edited, err = s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: edited.Invite.Hash, HasTitle: true, HasExpireDate: true, HasUsageLimit: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Invite.Permanent {
		t.Fatal("clearing parameters promoted an additional invite")
	}
	if _, err = s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: edited.Invite.Hash, HasExpireDate: true, ExpireDate: baseNow() + 3600, Date: baseNow()}); err != nil {
		t.Fatalf("additional invite cannot acquire expiry: %v", err)
	}
	revoked, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: edited.Invite.Hash, Revoked: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.NewInvite != nil {
		t.Fatal("revoking additional invite generated replacement")
	}
	rotated, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, LegacyRevokePermanent: true, Date: baseNow() + 1})
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: owner, ChannelID: id, Hash: main.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if !old.Revoked || !rotated.Invite.Permanent || rotated.Invite.Hash == main.Hash {
		t.Fatalf("invalid main rotation: old=%+v next=%+v", old, rotated.Invite)
	}
	replacement, err := s.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: owner, ChannelID: id, Hash: rotated.Invite.Hash, Revoked: true, Date: baseNow() + 2})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.NewInvite == nil || !replacement.NewInvite.Permanent || replacement.NewInvite.Revoked {
		t.Fatalf("main revoke has no active replacement: %+v", replacement)
	}
	listed, err := s.ListExportedInvites(ctx, domain.ChannelInviteListRequest{UserID: owner, ChannelID: id, AdminUserID: owner, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	mains := 0
	for _, invite := range listed.Invites {
		if invite.Permanent && !invite.Revoked {
			mains++
		}
	}
	if mains != 1 {
		t.Fatalf("active main links = %d, want 1", mains)
	}
}
