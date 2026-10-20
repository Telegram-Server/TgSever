package rpc

import (
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgChannelParticipant 是查询及 updateChannelParticipant 的共同投影：被踢、带封禁状态或
// view_messages 被禁的记录投成 channelParticipantBanned（不在群里时 left=true），退群的投成
// channelParticipantLeft，其余走普通成员投影。
func TestTgChannelParticipant(t *testing.T) {
	const self, user, actor = int64(1), int64(42), int64(7)
	for _, tc := range []struct {
		name   string
		member domain.ChannelMember
		want   tg.ChannelParticipantClass
	}{
		{
			name: "restricted non-member is banned with left=true",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberBanned, LeftAt: 300,
				BannedRights: domain.ChannelBannedRights{SendMedia: true, UntilDate: 900}},
			want: &tg.ChannelParticipantBanned{Left: true, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor, Date: 300,
				BannedRights: tg.ChatBannedRights{SendMedia: true, UntilDate: 900}},
		},
		{
			name: "kicked member is banned with left=true",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberKicked, LeftAt: 400,
				BannedRights: domain.ChannelBannedRights{ViewMessages: true, SendMessages: true}},
			want: &tg.ChannelParticipantBanned{Left: true, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor, Date: 400,
				BannedRights: tg.ChatBannedRights{ViewMessages: true, SendMessages: true}},
		},
		{
			name: "active member with view_messages banned stays left=false",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive,
				BannedRights: domain.ChannelBannedRights{ViewMessages: true}},
			want: &tg.ChannelParticipantBanned{Left: false, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor,
				BannedRights: tg.ChatBannedRights{ViewMessages: true}},
		},
		{
			name:   "left member",
			member: domain.ChannelMember{UserID: user, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft, LeftAt: 500},
			want:   &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: user}},
		},
		{
			name:   "plain member",
			member: domain.ChannelMember{UserID: user, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100},
			want:   &tg.ChannelParticipant{UserID: user, Date: 100},
		},
		{
			name:   "self",
			member: domain.ChannelMember{UserID: self, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100},
			want:   &tg.ChannelParticipantSelf{UserID: self, InviterID: actor, Date: 100},
		},
		{
			name:   "no user",
			member: domain.ChannelMember{Status: domain.ChannelMemberKicked},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tgChannelParticipant(self, tc.member)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tgChannelParticipant = %#v, want %#v", got, tc.want)
			}
		})
	}
}
