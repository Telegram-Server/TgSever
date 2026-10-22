package rpc

import (
	"slices"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// memberRequestsToJoin 让普通成员先退群，再通过需要审批的邀请链接提交入群申请。
func (f botAPIBridgeFixture) memberRequestsToJoin(t *testing.T) {
	t.Helper()
	if _, err := f.router.onChannelsLeaveChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), f.exportInvite(t, true)); err == nil {
		t.Fatal("request-needed invite joined directly")
	}
	if got := f.pendingRequesters(t); !slices.Equal(got, []int64{f.member.ID}) {
		t.Fatalf("pending requesters = %v, want the member", got)
	}
}

// pendingRequesters 返回群主视角的待审批申请人。
func (f botAPIBridgeFixture) pendingRequesters(t *testing.T) []int64 {
	t.Helper()
	pending, err := f.channels.PendingJoinRequests(f.ctx, f.channel.ID, domain.MaxChannelPendingJoinRecentRequesters)
	if err != nil {
		t.Fatalf("pending join requests: %v", err)
	}
	if pending.Count != len(pending.RecentRequesters) {
		t.Fatalf("pending = %+v", pending)
	}
	return pending.RecentRequesters
}

// lastPendingCountPushedTo 返回最近一次推给 userID 的待审批计数；没有推送时 ok=false。
func (f botAPIBridgeFixture) lastPendingCountPushedTo(userID int64) (int, bool) {
	count, ok := 0, false
	for _, push := range f.sessions.pushesTo(userID) {
		updates, isUpdates := push.(*tg.Updates)
		if !isUpdates {
			continue
		}
		for _, update := range updates.Updates {
			if pending, isPending := update.(*tg.UpdatePendingJoinRequests); isPending {
				count, ok = pending.RequestsPending, true
			}
		}
	}
	return count, ok
}

func TestBotAPIApproveChatJoinRequestAddsMemberAndNotifies(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	f.memberRequestsToJoin(t)
	f.sessions.clearMessages()

	if err := f.router.BotAPIApproveChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}

	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberActive {
		t.Fatalf("member after approve = %+v, want active", got)
	}
	if got := f.pendingRequesters(t); len(got) != 0 {
		t.Fatalf("pending requesters after approve = %v", got)
	}
	pushed := f.sessions.pushedUserIDs()
	if !slices.Contains(pushed, f.owner.ID) || !slices.Contains(pushed, f.member.ID) {
		t.Fatalf("pushed users = %v, want the owner and the applicant", pushed)
	}
	if count, ok := f.lastPendingCountPushedTo(f.owner.ID); !ok || count != 0 {
		t.Fatalf("owner pending count push = %d (pushed %v), want 0", count, ok)
	}
}

func TestBotAPIDeclineChatJoinRequestDropsRequest(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	f.memberRequestsToJoin(t)
	f.sessions.clearMessages()

	if err := f.router.BotAPIDeclineChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID); err != nil {
		t.Fatalf("decline: %v", err)
	}

	if got := f.participant(t, f.member.ID); got.Status == domain.ChannelMemberActive {
		t.Fatalf("declined applicant joined: %+v", got)
	}
	if got := f.pendingRequesters(t); len(got) != 0 {
		t.Fatalf("pending requesters after decline = %v", got)
	}
	if count, ok := f.lastPendingCountPushedTo(f.owner.ID); !ok || count != 0 {
		t.Fatalf("owner pending count push = %d (pushed %v), want 0", count, ok)
	}
}

// 不存在的申请与已经处理过的申请都报 HIDE_REQUESTER_MISSING。
func TestBotAPIChatJoinRequestMissingOrHandled(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})

	wantBotAPIError(t, f.router.BotAPIApproveChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID), "HIDE_REQUESTER_MISSING")
	wantBotAPIError(t, f.router.BotAPIDeclineChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID), "HIDE_REQUESTER_MISSING")

	f.memberRequestsToJoin(t)
	if err := f.router.BotAPIDeclineChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID); err != nil {
		t.Fatalf("decline: %v", err)
	}
	wantBotAPIError(t, f.router.BotAPIDeclineChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID), "HIDE_REQUESTER_MISSING")
	wantBotAPIError(t, f.router.BotAPIApproveChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID), "HIDE_REQUESTER_MISSING")
}

// 普通成员机器人和没有邀请权限的管理员机器人都不能处理申请，申请保持待审批。
func TestBotAPIChatJoinRequestRequiresInviteRight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		promote *domain.ChannelAdminRights
	}{
		{name: "member bot"},
		{name: "admin bot without invite right", promote: &domain.ChannelAdminRights{BanUsers: true, DeleteMessages: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			if tc.promote != nil {
				f.promoteBot(t, *tc.promote)
			}
			f.memberRequestsToJoin(t)

			wantBotAPIError(t, f.router.BotAPIApproveChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID), "CHAT_ADMIN_REQUIRED")
			wantBotAPIError(t, f.router.BotAPIDeclineChatJoinRequest(f.ctx, f.bot.ID, f.chatID, f.member.ID), "CHAT_ADMIN_REQUIRED")

			if got := f.pendingRequesters(t); !slices.Equal(got, []int64{f.member.ID}) {
				t.Fatalf("pending requesters = %v, want the member still pending", got)
			}
			if got := f.participant(t, f.member.ID); got.Status == domain.ChannelMemberActive {
				t.Fatalf("applicant joined without approval: %+v", got)
			}
		})
	}
}
