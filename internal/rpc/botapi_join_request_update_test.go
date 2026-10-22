package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// botJoinRequestEvents 取出机器人队列里的 chat_join_request（不确认 offset，可重复读）。
func botJoinRequestEvents(t *testing.T, f botAPIBridgeFixture) []domain.UpdateEvent {
	t.Helper()
	events, err := f.router.BotAPIUpdates(f.ctx, f.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	var out []domain.UpdateEvent
	for _, event := range events {
		if event.Type == domain.UpdateEventBotChatJoinRequest {
			if event.BotChatJoinRequest == nil {
				t.Fatalf("chat_join_request event without payload: %+v", event)
			}
			out = append(out, event)
		}
	}
	return out
}

// applyViaInviteLink 由 member 通过一条需要审批的邀请链接申请加入，返回链接 hash。
func applyViaInviteLink(t *testing.T, f botAPIBridgeFixture) string {
	t.Helper()
	hash := f.exportInvite(t, true)
	if _, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash); err == nil {
		t.Fatal("import of a request-needed invite joined directly")
	}
	return hash
}

// applyViaUsername 由群主开启入群审批后，member 通过公开用户名申请加入。
func applyViaUsername(t *testing.T, f botAPIBridgeFixture) string {
	t.Helper()
	if _, err := f.router.onChannelsToggleJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsToggleJoinRequestRequest{
		Channel: f.inputChannel(), Enabled: true,
	}); err != nil {
		t.Fatalf("toggle join request: %v", err)
	}
	if _, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err == nil {
		t.Fatal("joinChannel of a join-request group joined directly")
	}
	return ""
}

func TestBotAPIChatJoinRequestReachesInviteAdminBots(t *testing.T) {
	paths := []struct {
		name  string
		apply func(t *testing.T, f botAPIBridgeFixture) string
	}{
		{name: "invite link", apply: applyViaInviteLink},
		{name: "public username", apply: applyViaUsername},
	}
	bots := []struct {
		name    string
		setup   func(t *testing.T, f botAPIBridgeFixture)
		receive bool
	}{
		{name: "admin with invite right", receive: true, setup: func(t *testing.T, f botAPIBridgeFixture) {
			f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
		}},
		{name: "admin without invite right", setup: func(t *testing.T, f botAPIBridgeFixture) {
			f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, DeleteMessages: true})
		}},
		{name: "plain member bot", setup: func(*testing.T, botAPIBridgeFixture) {}},
	}
	for _, path := range paths {
		for _, bot := range bots {
			t.Run(path.name+"/"+bot.name, func(t *testing.T) {
				f := newBotAPIBridgeFixture(t)
				bot.setup(t, f)
				if _, err := f.users.UpdateProfile(f.ctx, f.member.ID, domain.UserProfileUpdate{About: "likes cats", HasAbout: true}); err != nil {
					t.Fatalf("set bio: %v", err)
				}
				f.memberLeaves(t)
				hash := path.apply(t, f)

				events := botJoinRequestEvents(t, f)
				if !bot.receive {
					if len(events) != 0 {
						t.Fatalf("bot without invite right got chat_join_request: %+v", events)
					}
					return
				}
				if len(events) != 1 {
					t.Fatalf("chat_join_request events = %+v, want one", events)
				}
				event := events[0]
				request := event.BotChatJoinRequest.Request
				chat := domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}
				if event.Peer != chat || event.BotAPIUpdateID <= 0 || request.Chat != chat ||
					request.UserID != f.member.ID || request.Date <= 0 || request.Bio != "likes cats" {
					t.Fatalf("chat_join_request = %+v payload %+v", event, request)
				}
				// 投影需要的申请人和群必须随事件带上。
				if !hasUser(event.Users, f.member.ID) || len(event.Channels) == 0 {
					t.Fatalf("event users %+v channels %+v", event.Users, event.Channels)
				}
				link := event.BotChatJoinRequest.InviteLink
				if hash == "" {
					if link != nil {
						t.Fatalf("username request carries invite_link %+v", link)
					}
					return
				}
				if link == nil || link.Invite.Hash != hash || !link.Invite.RequestNeeded ||
					link.Link != publicLinkWithBaseURL(f.router.cfg.PublicBaseURL, "+"+hash) ||
					link.Creator.ID != f.owner.ID || link.Creator.FirstName != "Owner" {
					t.Fatalf("invite_link = %+v, want the link %q created by the owner", link, hash)
				}
			})
		}
	}
}

func TestBotAPIChatJoinRequestFollowsAllowedUpdates(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessage}); err != nil {
		t.Fatalf("set allowed updates: %v", err)
	}
	f.memberLeaves(t)
	applyViaInviteLink(t, f)
	if got := botJoinRequestEvents(t, f); len(got) != 0 {
		t.Fatalf("chat_join_request outside allowed_updates delivered: %+v", got)
	}

	// 群主拒绝这次申请后成员可以再申请；回到默认集合（空列表）后，这次照常投递。
	if _, err := f.router.onMessagesHideChatJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		UserID: &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash},
	}); err != nil {
		t.Fatalf("decline: %v", err)
	}
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, nil); err != nil {
		t.Fatalf("reset allowed updates: %v", err)
	}
	applyViaUsername(t, f)
	if got := botJoinRequestEvents(t, f); len(got) != 1 {
		t.Fatalf("default allowed_updates chat_join_request = %+v, want one", got)
	}
}

// 更新进入持久队列：没确认之前再次 getUpdates 仍能拿到，确认之后才消失。
func TestBotAPIChatJoinRequestStaysQueuedUntilConfirmed(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	f.memberLeaves(t)
	applyViaInviteLink(t, f)

	first := botJoinRequestEvents(t, f)
	again := botJoinRequestEvents(t, f)
	if len(first) != 1 || len(again) != 1 || again[0].BotAPIUpdateID != first[0].BotAPIUpdateID {
		t.Fatalf("first read %+v, second read %+v, want the same queued request", first, again)
	}
	events, err := f.router.BotAPIUpdates(f.ctx, f.bot.ID, first[0].BotAPIUpdateID+1)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	for _, event := range events {
		if event.Type == domain.UpdateEventBotChatJoinRequest {
			t.Fatalf("confirmed chat_join_request returned again: %+v", event)
		}
	}
}

// 申请已在待审批时重复申请（同一链接 / 公开用户名）不再入队新的 chat_join_request（review #2）。
func TestBotAPIChatJoinRequestNotRequeuedForPendingRequest(t *testing.T) {
	t.Run("invite link", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
		f.memberLeaves(t)
		hash := applyViaInviteLink(t, f)
		if _, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash); err == nil {
			t.Fatal("repeat import joined directly")
		}
		if got := botJoinRequestEvents(t, f); len(got) != 1 {
			t.Fatalf("chat_join_request events after repeat = %d, want 1", len(got))
		}
	})
	t.Run("public username", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
		f.memberLeaves(t)
		applyViaUsername(t, f)
		if _, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err == nil {
			t.Fatal("repeat joinChannel joined directly")
		}
		if got := botJoinRequestEvents(t, f); len(got) != 1 {
			t.Fatalf("chat_join_request events after repeat = %d, want 1", len(got))
		}
	})
}
