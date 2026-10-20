package storetest

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

type BotDefaultAdminRightsFixture struct {
	Bots  store.BotStore
	BotID int64
}

func BotDefaultAdminRights(t *testing.T, newFixture func(*testing.T) BotDefaultAdminRightsFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("scopes persist independently and unchanged writes are detected", func(t *testing.T) {
		f := newFixture(t)
		group := domain.ChannelAdminRights{BanUsers: true, DeleteMessages: true, ManageChat: true, ManageTopics: true, ManageRanks: true}
		broadcast := domain.ChannelAdminRights{PostMessages: true, EditStories: true, ManageDirectMessages: true}
		for _, step := range []struct {
			broadcast bool
			rights    domain.ChannelAdminRights
			changed   bool
		}{
			{false, domain.ChannelAdminRights{}, false}, {true, domain.ChannelAdminRights{}, false},
			{false, group, true}, {true, broadcast, true}, {false, group, false}, {true, broadcast, false},
		} {
			changed, err := f.Bots.SetBotDefaultAdminRights(ctx, f.BotID, step.broadcast, step.rights)
			if err != nil || changed != step.changed {
				t.Fatalf("set broadcast=%v = %v,%v want changed=%v", step.broadcast, changed, err, step.changed)
			}
		}
		p, found, err := f.Bots.GetBot(ctx, f.BotID)
		if err != nil || !found || p.GroupAdminRights != group || p.BroadcastAdminRights != broadcast {
			t.Fatalf("persisted rights = %+v found=%v err=%v", p, found, err)
		}
		changed, err := f.Bots.SetBotDefaultAdminRights(ctx, f.BotID, false, domain.ChannelAdminRights{})
		if err != nil || !changed {
			t.Fatalf("clear group = %v,%v", changed, err)
		}
		p, found, err = f.Bots.GetBot(ctx, f.BotID)
		if err != nil || !found || p.GroupAdminRights != (domain.ChannelAdminRights{}) || p.BroadcastAdminRights != broadcast {
			t.Fatalf("cleared rights = %+v found=%v err=%v", p, found, err)
		}
	})
	t.Run("unknown bots rejected", func(t *testing.T) {
		f := newFixture(t)
		if _, err := f.Bots.SetBotDefaultAdminRights(ctx, f.BotID+1000000000, false, domain.ChannelAdminRights{BanUsers: true}); !errors.Is(err, domain.ErrBotNotFound) {
			t.Fatalf("unknown bot: %v", err)
		}
	})
}
