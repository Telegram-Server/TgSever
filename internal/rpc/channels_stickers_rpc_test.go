package rpc

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestChannelStickerSetsRPC(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	ids := make([]int64, 150)
	for i := range ids {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(i + 1), Phone: fmt.Sprintf("1555555%04d", i), FirstName: "Sticker member", Bot: i == 1})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = u.ID
	}
	owner, bot, member := ids[0], ids[1], ids[2]
	channels := memory.NewChannelStore()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Group stickers", Megagroup: true, MemberUserIDs: ids[1:]})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: bot, AdminRights: domain.ChannelAdminRights{ChangeInfo: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: ids[3], AdminRights: domain.ChannelAdminRights{InviteUsers: true}}); err != nil {
		t.Fatal(err)
	}
	sessions := &captureSessions{onlineUserIDs: []int64{owner, bot, member}, channelMembers: map[int64][]int64{id: {owner, bot, member}}}
	files := &fakeFiles{sets: map[domain.StickerSetKind][]domain.StickerSet{domain.StickerSetKindStickers: {{ID: 10, AccessHash: 100, ShortName: "group_pack", Title: "Group Pack", Kind: domain.StickerSetKindStickers, Count: 1}}, domain.StickerSetKindEmoji: {{ID: 20, AccessHash: 200, ShortName: "emoji_pack", Kind: domain.StickerSetKindEmoji, Emojis: true}}}}
	r := New(Config{}, Deps{Users: appusers.NewService(users), Channels: appchannels.NewService(channels), Files: files, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	input := &tg.InputChannel{ChannelID: id, AccessHash: created.Channel.AccessHash}
	full := func(user int64) *tg.ChannelFull {
		t.Helper()
		v, err := r.onChannelsGetFullChannel(WithUserID(ctx, user), input)
		if err != nil {
			t.Fatal(err)
		}
		return v.FullChat.(*tg.ChannelFull)
	}
	set := func(user int64, pack tg.InputStickerSetClass) error {
		t.Helper()
		ok, err := r.onChannelsSetStickers(WithUserID(ctx, user), &tg.ChannelsSetStickersRequest{Channel: input, Stickerset: pack})
		if err == nil && !ok {
			t.Fatal("successful setStickers returned false")
		}
		return err
	}
	assertSet := func(want int64) {
		t.Helper()
		s, ok := full(member).GetStickerset()
		if want == 0 {
			if ok {
				t.Fatalf("removed sticker set still visible: %+v", s)
			}
		} else if !ok || s.ID != want || s.ShortName != "group_pack" {
			t.Fatalf("group sticker set = %+v present %v", s, ok)
		}
	}
	assertUpdate := func() {
		t.Helper()
		if !slices.Contains(sessions.pushedUserIDs(), member) {
			t.Fatal("member did not receive setting update")
		}
		pushed, ok := sessions.lastUserPush().(*tg.Updates)
		if !ok {
			t.Fatalf("push = %T", sessions.lastUserPush())
		}
		found := false
		for _, update := range pushed.Updates {
			if u, ok := update.(*tg.UpdateChannel); ok && u.ChannelID == id {
				found = true
			}
		}
		if !found {
			t.Fatal("setting update missing updateChannel")
		}
	}
	t.Run("capability follows change_info", func(t *testing.T) {
		if !full(owner).CanSetStickers || !full(bot).CanSetStickers || full(member).CanSetStickers || full(ids[3]).CanSetStickers {
			t.Fatal("can_set_stickers does not follow permissions")
		}
	})
	t.Run("member and unrelated admin rejected", func(t *testing.T) {
		for _, user := range []int64{member, ids[3]} {
			if err := set(user, &tg.InputStickerSetID{ID: 10, AccessHash: 100}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
				t.Fatalf("unauthorized set = %v", err)
			}
		}
	})
	t.Run("bot sets by short name and members see update", func(t *testing.T) {
		sessions.clearMessages()
		if err := set(bot, &tg.InputStickerSetShortName{ShortName: "group_pack"}); err != nil {
			t.Fatal(err)
		}
		assertSet(10)
		assertUpdate()
	})
	t.Run("empty removes and id restores", func(t *testing.T) {
		sessions.clearMessages()
		if err := set(bot, &tg.InputStickerSetEmpty{}); err != nil {
			t.Fatal(err)
		}
		assertSet(0)
		assertUpdate()
		if err := set(owner, &tg.InputStickerSetID{ID: 10, AccessHash: 100}); err != nil {
			t.Fatal(err)
		}
		assertSet(10)
	})
	t.Run("invalid sets leave state unchanged", func(t *testing.T) {
		for _, pack := range []tg.InputStickerSetClass{&tg.InputStickerSetID{ID: 10, AccessHash: 999}, &tg.InputStickerSetShortName{ShortName: "missing"}, &tg.InputStickerSetID{ID: 20, AccessHash: 200}} {
			if err := set(owner, pack); !tgerr.Is(err, "STICKERSET_INVALID") {
				t.Fatalf("invalid set %T = %v", pack, err)
			}
			assertSet(10)
		}
	})
	// Telegram documents the 100-member boundary, while TDLib/tdesktop use
	// channelFull.can_set_stickers: https://telegram.org/blog/replies-mentions-stickers#stickers-of-the-group
	// https://github.com/tdlib/td/blob/master/td/telegram/ChatManager.cpp (set_channel_sticker_set).
	t.Run("80 members can remove but cannot configure a set", func(t *testing.T) {
		for _, user := range ids[80:] {
			if _, err := channels.LeaveChannel(ctx, id, user, 0); err != nil {
				t.Fatal(err)
			}
		}
		r.invalidateRPCProjectionForChannel(id)
		if !full(bot).CanSetStickers || full(member).CanSetStickers || full(ids[3]).CanSetStickers {
			t.Fatal("configured small group deletion capability does not follow change_info")
		}
		sessions.clearMessages()
		if err := set(bot, &tg.InputStickerSetID{ID: 10, AccessHash: 100}); !tgerr.Is(err, "PARTICIPANTS_TOO_FEW") {
			t.Fatalf("80-member configuration = %v", err)
		}
		for _, user := range []int64{member, ids[3]} {
			if err := set(user, &tg.InputStickerSetEmpty{}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
				t.Fatalf("unauthorized removal = %v", err)
			}
		}
		assertSet(10)
		if len(sessions.pushedUserIDs()) != 0 {
			t.Fatal("rejected mutation pushed an update")
		}
		if err := set(bot, &tg.InputStickerSetEmpty{}); err != nil {
			t.Fatal(err)
		}
		assertSet(0)
		assertUpdate()
		if full(bot).CanSetStickers {
			t.Fatal("empty small group advertises configuration capability")
		}
	})
}
