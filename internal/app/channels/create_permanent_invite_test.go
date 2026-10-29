package channels

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestCreateChannelReturnsCreatorPermanentInvite(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewChannelStore())
	const creator = int64(1001)
	res, err := svc.CreateChannel(ctx, creator, domain.CreateChannelRequest{
		Title:     "迁移群",
		Megagroup: true,
		Date:      1_700_000_000,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if res.PermanentInvite == nil {
		t.Fatal("PermanentInvite = nil, want creator's permanent invite")
	}
	list, err := svc.ListExportedInvites(ctx, creator, domain.ChannelInviteListRequest{
		ChannelID:   res.Channel.ID,
		AdminUserID: creator,
	})
	if err != nil {
		t.Fatalf("ListExportedInvites: %v", err)
	}
	var permanent []domain.ChannelInvite
	for _, invite := range list.Invites {
		if invite.Permanent && !invite.Revoked {
			permanent = append(permanent, invite)
		}
	}
	if len(permanent) != 1 {
		t.Fatalf("permanent invites = %+v, want exactly one", permanent)
	}
	got := *res.PermanentInvite
	if got.Hash == "" || got.Hash != permanent[0].Hash || got.InviteID != permanent[0].InviteID {
		t.Fatalf("PermanentInvite = %+v, want the listed permanent invite %+v", got, permanent[0])
	}
	if got.ChannelID != res.Channel.ID || got.AdminUserID != creator || !got.Permanent {
		t.Fatalf("PermanentInvite = %+v, want creator's permanent invite of channel %d", got, res.Channel.ID)
	}
}

// failingPermanentInviteStore 让永久邀请生成失败，其余照常走内存 store。
type failingPermanentInviteStore struct {
	store.ChannelStore
}

func (failingPermanentInviteStore) EnsurePermanentInvite(context.Context, int64, int64, int) (domain.ChannelInvite, error) {
	return domain.ChannelInvite{}, errors.New("invite store down")
}

func TestCreateChannelSucceedsWithoutPermanentInviteWhenEnsureFails(t *testing.T) {
	ctx := context.Background()
	svc := NewService(failingPermanentInviteStore{ChannelStore: memory.NewChannelStore()})
	res, err := svc.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "迁移群",
		Megagroup: true,
		Date:      1_700_000_000,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v, want success despite invite failure", err)
	}
	if res.Channel.ID == 0 {
		t.Fatal("channel not created")
	}
	if res.PermanentInvite != nil {
		t.Fatalf("PermanentInvite = %+v, want nil when generation failed", res.PermanentInvite)
	}
}
