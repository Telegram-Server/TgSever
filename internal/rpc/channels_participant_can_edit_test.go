package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 官方客户端靠 channelParticipantAdmin.can_edit 决定是否显示"编辑管理员"入口：群主可编辑
// 所有管理员，有 add_admins 的管理员只能编辑自己提拔的人。
func TestChannelParticipantAdminCarriesCanEdit(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true})
	// 机器人提拔 member：member 的提拔人是机器人。
	if _, err := f.channels.EditAdmin(f.ctx, f.bot.ID, domain.EditChannelAdminRequest{
		UserID: f.bot.ID, ChannelID: f.channel.ID, MemberID: f.member.ID,
		AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: 30,
	}); err != nil {
		t.Fatalf("bot promotes member: %v", err)
	}
	input := &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	canEdit := func(t *testing.T, viewer, target domain.User) bool {
		t.Helper()
		res, err := f.router.onChannelsGetParticipant(WithUserID(f.ctx, viewer.ID), &tg.ChannelsGetParticipantRequest{
			Channel:     input,
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

	for _, tc := range []struct {
		name           string
		viewer, target domain.User
		want           bool
	}{
		{"owner edits any admin", f.owner, f.bot, true},
		{"owner edits a grand-promoted admin", f.owner, f.member, true},
		{"promoter edits own promotee", f.bot, f.member, true},
		{"promotee cannot edit promoter", f.member, f.bot, false},
	} {
		if got := canEdit(t, tc.viewer, tc.target); got != tc.want {
			t.Fatalf("%s: can_edit = %v, want %v", tc.name, got, tc.want)
		}
	}

	list, err := f.router.onChannelsGetParticipants(WithUserID(f.ctx, f.bot.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: input, Filter: &tg.ChannelParticipantsAdmins{}, Limit: 10,
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
