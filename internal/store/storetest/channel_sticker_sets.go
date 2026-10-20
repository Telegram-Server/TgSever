package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelStickerSets verifies authorization, the 99/100 boundary, and durable
// set/removal through fresh store instances for both supported backends.
func ChannelStickerSets(t *testing.T, s store.ChannelStore, reopen func() store.ChannelStore, users []int64) {
	t.Helper()
	ctx := context.Background()
	owner, admin, member := users[0], users[1], users[2]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Sticker contract", Megagroup: true, MemberUserIDs: users[1:99], Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: admin, AdminRights: domain.ChannelAdminRights{ChangeInfo: true}, Date: baseNow()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStickers(ctx, member, id, 71); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member setting = %v", err)
	}
	if _, err := s.SetStickers(ctx, admin, id, 71); !errors.Is(err, domain.ErrChannelParticipantsTooFew) {
		t.Fatalf("99-member setting = %v", err)
	}
	if _, err := s.InviteToChannel(ctx, id, owner, users[99:], baseNow()); err != nil {
		t.Fatal(err)
	}
	for _, pack := range []int64{71, 72, 0} {
		if _, err := s.SetStickers(ctx, admin, id, pack); err != nil {
			t.Fatal(err)
		}
		s = reopen()
		view, err := s.GetChannel(ctx, member, id)
		if err != nil {
			t.Fatal(err)
		}
		if view.Channel.StickerSetID != pack {
			t.Fatalf("durable sticker set = %d, want %d", view.Channel.StickerSetID, pack)
		}
	}
	if _, err := s.SetStickers(ctx, owner, id, 73); err != nil {
		t.Fatal(err)
	}
	for _, user := range users[80:] {
		if _, err := s.LeaveChannel(ctx, id, user, baseNow()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetStickers(ctx, owner, id, 74); !errors.Is(err, domain.ErrChannelParticipantsTooFew) {
		t.Fatalf("setting below member threshold = %v", err)
	}
	if _, err := s.SetStickers(ctx, member, id, 0); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("unauthorized removal = %v", err)
	}
	if _, err := s.SetStickers(ctx, admin, id, 0); err != nil {
		t.Fatalf("removing below member threshold = %v", err)
	}
	s = reopen()
	view, err := s.GetChannel(ctx, member, id)
	if err != nil || view.Channel.StickerSetID != 0 {
		t.Fatalf("durable removal = %+v, %v", view.Channel, err)
	}
	broadcast, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Not a supergroup", Broadcast: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStickers(ctx, owner, broadcast.Channel.ID, 0); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("broadcast setting = %v", err)
	}
}
