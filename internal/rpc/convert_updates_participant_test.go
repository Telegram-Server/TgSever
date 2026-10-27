package rpc

import (
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgChannelParticipant 是 domain 成员 → TL 成员的唯一转换。判断顺序：
//  1. 被踢出或带 view_messages 禁令 → channelParticipantBanned left=true；
//  2. 不在群里但受限 → channelParticipantBanned left=true；
//  3. 在群且限制不为空 → channelParticipantBanned left=false（本人也一样）；
//  4. 已离开 → channelParticipantLeft；
//  5. 其余按角色：creator / admin / self / 普通成员。
//
// 结果只取决于成员记录和"是否本人"，不按查看者是否为管理员裁剪。
func TestTgChannelParticipant(t *testing.T) {
	const self, user, actor, inviter = int64(1), int64(42), int64(7), int64(5)
	mute := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: 900}
	kick := domain.ChannelBannedRights{ViewMessages: true, SendMessages: true}
	for _, tc := range []struct {
		name   string
		viewer int64
		member domain.ChannelMember
		want   tg.ChannelParticipantClass
	}{
		// 规则 1：被踢出。
		{
			name: "kicked member is banned with left=true",
			member: domain.ChannelMember{UserID: user, InviterUserID: inviter, RestrictedBy: actor, RestrictedAt: 390, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberKicked, LeftAt: 400,
				BannedRights: kick},
			want: &tg.ChannelParticipantBanned{Left: true, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor, Date: 390,
				BannedRights: tg.ChatBannedRights{ViewMessages: true, SendMessages: true}},
		},
		{
			name: "kicked self is banned, not channelParticipantSelf",
			member: domain.ChannelMember{UserID: self, InviterUserID: inviter, RestrictedBy: actor, RestrictedAt: 390, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberKicked, LeftAt: 400,
				BannedRights: kick},
			want: &tg.ChannelParticipantBanned{Left: true, Peer: &tg.PeerUser{UserID: self}, KickedBy: actor, Date: 390,
				BannedRights: tg.ChatBannedRights{ViewMessages: true, SendMessages: true}},
		},
		{
			name: "active record with view_messages ban is kicked: left=true",
			member: domain.ChannelMember{UserID: user, InviterUserID: inviter, RestrictedBy: actor, RestrictedAt: 380, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive,
				BannedRights: domain.ChannelBannedRights{ViewMessages: true}},
			want: &tg.ChannelParticipantBanned{Left: true, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor, Date: 380,
				BannedRights: tg.ChatBannedRights{ViewMessages: true}},
		},
		// 规则 2：不在群里但受限。
		{
			name: "restricted non-member is banned with left=true",
			member: domain.ChannelMember{UserID: user, InviterUserID: inviter, RestrictedBy: actor, RestrictedAt: 310, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberBanned, LeftAt: 300,
				BannedRights: mute},
			want: &tg.ChannelParticipantBanned{Left: true, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor, Date: 310,
				BannedRights: tg.ChatBannedRights{SendMessages: true, SendMedia: true, UntilDate: 900}},
		},
		// 规则 3：在群受限。
		{
			name: "restriction backfilled from before the actor columns has no date",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, RestrictedBy: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100,
				BannedRights: mute},
			want: &tg.ChannelParticipantBanned{Left: false, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor,
				BannedRights: tg.ChatBannedRights{SendMessages: true, SendMedia: true, UntilDate: 900}},
		},
		{
			name: "active member with a partial restriction is banned with left=false",
			member: domain.ChannelMember{UserID: user, InviterUserID: inviter, RestrictedBy: actor, RestrictedAt: 250, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100,
				BannedRights: mute},
			want: &tg.ChannelParticipantBanned{Left: false, Peer: &tg.PeerUser{UserID: user}, KickedBy: actor, Date: 250,
				BannedRights: tg.ChatBannedRights{SendMessages: true, SendMedia: true, UntilDate: 900}},
		},
		{
			name: "restricted self is banned with left=false, not channelParticipantSelf",
			member: domain.ChannelMember{UserID: self, InviterUserID: inviter, RestrictedBy: actor, RestrictedAt: 250, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100,
				BannedRights: mute},
			want: &tg.ChannelParticipantBanned{Left: false, Peer: &tg.PeerUser{UserID: self}, KickedBy: actor, Date: 250,
				BannedRights: tg.ChatBannedRights{SendMessages: true, SendMedia: true, UntilDate: 900}},
		},
		{
			name: "a record that only carries an until date is a plain participant",
			member: domain.ChannelMember{UserID: user, InviterUserID: inviter, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100,
				BannedRights: domain.ChannelBannedRights{UntilDate: 900}},
			want: &tg.ChannelParticipant{UserID: user, Date: 100},
		},
		// 规则 4：已离开。
		{
			name:   "left member",
			member: domain.ChannelMember{UserID: user, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft, LeftAt: 500},
			want:   &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: user}},
		},
		{
			name:   "left self",
			member: domain.ChannelMember{UserID: self, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft, LeftAt: 500},
			want:   &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: self}},
		},
		{
			name:   "left admin (deleted account keeps its role) is left",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberLeft, LeftAt: 500},
			want:   &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: user}},
		},
		// 规则 5：按角色。
		{
			name:   "creator",
			member: domain.ChannelMember{UserID: user, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive, Rank: "boss"},
			want:   &tg.ChannelParticipantCreator{UserID: user, AdminRights: tgChatAdminRights(creatorProjectionAdminRights(domain.ChannelAdminRights{})), Rank: "boss", Flags: 1},
		},
		{
			name: "admin seen by someone else",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, JoinedAt: 100,
				AdminRights: domain.ChannelAdminRights{DeleteMessages: true}},
			want: &tg.ChannelParticipantAdmin{UserID: user, PromotedBy: actor, InviterID: actor, Date: 100, Flags: 2,
				AdminRights: tg.ChatAdminRights{DeleteMessages: true, Other: true}},
		},
		{
			name: "admin seen by itself",
			member: domain.ChannelMember{UserID: self, InviterUserID: actor, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, JoinedAt: 100,
				AdminRights: domain.ChannelAdminRights{DeleteMessages: true}},
			want: &tg.ChannelParticipantAdmin{Self: true, UserID: self, PromotedBy: actor, InviterID: actor, Date: 100, Flags: 2,
				AdminRights: tg.ChatAdminRights{DeleteMessages: true, Other: true}},
		},
		{
			name:   "plain member",
			member: domain.ChannelMember{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100},
			want:   &tg.ChannelParticipant{UserID: user, Date: 100},
		},
		{
			name:   "self",
			member: domain.ChannelMember{UserID: self, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100},
			want:   &tg.ChannelParticipantSelf{UserID: self, InviterID: actor, Date: 100},
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

// 推送路径（updateChannelParticipant、bot 的 chat_member、admin log）与列表共用同一份转换，
// 只多一条：用户 id 为 0 的记录不输出。
func TestTgChannelParticipantForUpdate(t *testing.T) {
	const self, user, actor = int64(1), int64(42), int64(7)
	if got := tgChannelParticipantForUpdate(self, domain.ChannelMember{Status: domain.ChannelMemberKicked}); got != nil {
		t.Fatalf("no-user record = %#v, want nil", got)
	}
	for _, member := range []domain.ChannelMember{
		{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100,
			BannedRights: domain.ChannelBannedRights{SendMedia: true}},
		{UserID: user, InviterUserID: actor, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberKicked, LeftAt: 400,
			BannedRights: domain.ChannelBannedRights{ViewMessages: true}},
		{UserID: user, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft},
		{UserID: self, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: 100},
	} {
		if got, want := tgChannelParticipantForUpdate(self, member), tgChannelParticipant(self, member); !reflect.DeepEqual(got, want) {
			t.Fatalf("update projection of %+v = %#v, want list projection %#v", member, got, want)
		}
	}
}
