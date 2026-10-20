package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"testing"
)

func TestPersonalChannelConstraintsRPC(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(571, "15550000571", "Owner")
	viewer := f.user(572, "15550000572", "Viewer")
	create := func(by domain.User, mega, public bool, name string) *tg.Channel {
		t.Helper()
		result, err := f.router.onChannelsCreateChannel(f.userCtx(by), &tg.ChannelsCreateChannelRequest{Title: name, Megagroup: mega, Broadcast: !mega})
		if err != nil {
			t.Fatal(err)
		}
		ch := result.(*tg.Updates).Chats[0].(*tg.Channel)
		if public {
			if _, err := f.router.onChannelsUpdateUsername(f.userCtx(by), &tg.ChannelsUpdateUsernameRequest{Channel: inputChannel(ch), Username: name}); err != nil {
				t.Fatal(err)
			}
		}
		return ch
	}
	group := create(owner, true, true, "personal_group")
	private := create(owner, false, false, "personal_private")
	foreign := create(viewer, false, true, "personal_foreign")
	if _, err := f.router.onChannelsInviteToChannel(f.userCtx(viewer), &tg.ChannelsInviteToChannelRequest{Channel: inputChannel(foreign), Users: []tg.InputUserClass{inputUser(owner)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.onChannelsEditAdmin(f.userCtx(viewer), &tg.ChannelsEditAdminRequest{Channel: inputChannel(foreign), UserID: inputUser(owner), AdminRights: tg.ChatAdminRights{ChangeInfo: true, PostMessages: true}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ch   *tg.Channel
	}{{"megagroup rejected", group}, {"private broadcast rejected", private}, {"other creator even when administrator rejected", foreign}} {
		t.Run(tc.name, func(t *testing.T) {
			if ok, err := f.router.onAccountUpdatePersonalChannel(f.userCtx(owner), inputChannel(tc.ch)); err == nil || ok {
				t.Fatalf("set personal channel = %v, %v", ok, err)
			}
		})
	}
	public := create(owner, false, true, "personal_public")
	if ok, err := f.router.onAccountUpdatePersonalChannel(f.userCtx(owner), inputChannel(public)); err != nil || !ok {
		t.Fatalf("set valid personal channel = %v,%v", ok, err)
	}
	posted, err := f.router.onMessagesSendMessage(f.userCtx(owner), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(public), Message: "latest personal post", RandomID: 57001})
	if err != nil {
		t.Fatal(err)
	}
	latestID := newMessageFromUpdates(t, posted).ID
	t.Run("public broadcast and latest post visible to nonmember", func(t *testing.T) {
		full, err := f.router.onUsersGetFullUser(f.userCtx(viewer), inputUser(owner))
		if err != nil {
			t.Fatal(err)
		}
		id, ok := full.FullUser.GetPersonalChannelID()
		if !ok || id != public.ID {
			t.Fatalf("personal channel = %d present=%v", id, ok)
		}
		msg, ok := full.FullUser.GetPersonalChannelMessage()
		if !ok || msg != latestID {
			t.Fatalf("latest post = %d present=%v want=%d", msg, ok, latestID)
		}
		if len(full.Chats) != 1 || full.Chats[0].(*tg.Channel).ID != public.ID {
			t.Fatalf("personal chats = %+v", full.Chats)
		}
	})
	t.Run("lost administration hides personal channel even with warmed projection", func(t *testing.T) {
		if _, err := f.router.onChannelsInviteToChannel(f.userCtx(owner), &tg.ChannelsInviteToChannelRequest{Channel: inputChannel(public), Users: []tg.InputUserClass{inputUser(viewer)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.router.onChannelsEditAdmin(f.userCtx(owner), &tg.ChannelsEditAdminRequest{Channel: inputChannel(public), UserID: inputUser(viewer), AdminRights: tgChatAdminRights(domain.CreatorChannelAdminRights())}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.router.onChannelsLeaveChannel(f.userCtx(owner), inputChannel(public)); err != nil {
			t.Fatal(err)
		}
		full, err := f.router.onUsersGetFullUser(f.userCtx(viewer), inputUser(owner))
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := full.FullUser.GetPersonalChannelID(); ok || id != 0 {
			t.Fatalf("stale personal channel = %d present=%v", id, ok)
		}
		if len(full.Chats) != 0 {
			t.Fatalf("stale personal chats = %+v", full.Chats)
		}
	})
}
