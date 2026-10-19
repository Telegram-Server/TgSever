package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 官方客户端靠 channelParticipantAdmin.can_edit 决定是否显示"编辑管理员"入口。规则本身
// （谁能编辑谁）由 domain.CanEditChannelAdmin 覆盖，这里只验 getParticipant/getParticipants
// 把它投影到 TL 上。
func TestChannelParticipantAdminCarriesCanEdit(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true})
	// 机器人提拔 member：member 的提拔人是机器人。
	f.promote(t, f.bot.ID, f.member.ID, domain.ChannelAdminRights{BanUsers: true}, "")
	canEdit := func(t *testing.T, viewer, target domain.User) bool {
		t.Helper()
		res, err := f.router.onChannelsGetParticipant(WithUserID(f.ctx, viewer.ID), &tg.ChannelsGetParticipantRequest{
			Channel:     f.inputChannel(),
			Participant: &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
		})
		if err != nil {
			t.Fatalf("getParticipant(%d as %d): %v", target.ID, viewer.ID, err)
		}
		admin, ok := res.Participant.(*tg.ChannelParticipantAdmin)
		if !ok {
			t.Fatalf("participant = %#v, want admin", res.Participant)
		}
		return admin.CanEdit
	}

	if !canEdit(t, f.bot, f.member) {
		t.Fatal("getParticipant: promoter sees can_edit = false on its promotee, want true")
	}
	if canEdit(t, f.member, f.bot) {
		t.Fatal("getParticipant: promotee sees can_edit = true on its promoter, want false")
	}

	list, err := f.router.onChannelsGetParticipants(WithUserID(f.ctx, f.bot.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: f.inputChannel(), Filter: &tg.ChannelParticipantsAdmins{}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("getParticipants: %v", err)
	}
	participants := list.(*tg.ChannelsChannelParticipants).Participants
	var sawMember bool
	for _, p := range participants {
		if admin, ok := p.(*tg.ChannelParticipantAdmin); ok && admin.UserID == f.member.ID {
			sawMember = true
			if !admin.CanEdit {
				t.Fatalf("admin list as promoter: member can_edit = false, want true")
			}
		}
	}
	if !sawMember {
		t.Fatalf("admin list = %#v, want the promoted member", participants)
	}
}
