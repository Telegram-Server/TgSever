package rpc

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 成员类型映射的协议面（#165）：被踢出、在群受限的成员经 getParticipant / getParticipants /
// bot 的 chat_member 推送都是 channelParticipantBanned。判断顺序本身在 TestTgChannelParticipant。

type bannedParticipantFixture struct {
	*adminBotAudienceFixture
	owner, member, other domain.User
	channel              domain.Channel
}

func newBannedParticipantFixture(t *testing.T) *bannedParticipantFixture {
	t.Helper()
	fx := newAdminBotAudienceFixture(t)
	owner, member, other := fx.newUser(t, 16501, false), fx.newUser(t, 16502, false), fx.newUser(t, 16503, false)
	now := int(time.Now().Unix())
	created, err := fx.channels.CreateChannel(fx.userCtx(owner), owner.ID, domain.CreateChannelRequest{Title: "banned mapping", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.channels.InviteToChannel(fx.userCtx(owner), owner.ID, created.Channel.ID, []int64{member.ID, other.ID}, now); err != nil {
		t.Fatalf("invite: %v", err)
	}
	return &bannedParticipantFixture{adminBotAudienceFixture: fx, owner: owner, member: member, other: other, channel: created.Channel}
}

func (f *bannedParticipantFixture) inputChannel() *tg.InputChannel {
	return &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

func (f *bannedParticipantFixture) editBanned(t *testing.T, target domain.User, rights tg.ChatBannedRights) {
	t.Helper()
	if _, err := f.router.onChannelsEditBanned(f.userCtx(f.owner), &tg.ChannelsEditBannedRequest{
		Channel:      f.inputChannel(),
		Participant:  &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
		BannedRights: rights,
	}); err != nil {
		t.Fatalf("editBanned: %v", err)
	}
}

func (f *bannedParticipantFixture) getParticipant(t *testing.T, viewer, target domain.User) tg.ChannelParticipantClass {
	t.Helper()
	got, err := f.router.onChannelsGetParticipant(f.userCtx(viewer), &tg.ChannelsGetParticipantRequest{
		Channel:     f.inputChannel(),
		Participant: &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
	})
	if err != nil {
		t.Fatalf("getParticipant: %v", err)
	}
	return got.Participant
}

func TestChannelsGetParticipantKickedUserIsBannedLeft(t *testing.T) {
	fx := newBannedParticipantFixture(t)
	fx.editBanned(t, fx.member, tg.ChatBannedRights{ViewMessages: true})

	banned, ok := fx.getParticipant(t, fx.owner, fx.member).(*tg.ChannelParticipantBanned)
	if !ok {
		t.Fatalf("kicked participant = %T, want channelParticipantBanned", fx.getParticipant(t, fx.owner, fx.member))
	}
	if !banned.Left || !banned.BannedRights.ViewMessages || banned.KickedBy != fx.owner.ID {
		t.Fatalf("kicked participant = %+v, want left=true view_messages kicked_by=%d", banned, fx.owner.ID)
	}
}

func TestChannelsGetParticipantsBannedFilterReturnsRestrictedMemberInGroup(t *testing.T) {
	fx := newBannedParticipantFixture(t)
	fx.editBanned(t, fx.member, tg.ChatBannedRights{SendMessages: true})

	got, err := fx.router.onChannelsGetParticipants(fx.userCtx(fx.owner), &tg.ChannelsGetParticipantsRequest{
		Channel: fx.inputChannel(),
		Filter:  &tg.ChannelParticipantsBanned{},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("getParticipants: %v", err)
	}
	list := got.(*tg.ChannelsChannelParticipants)
	if len(list.Participants) != 1 {
		t.Fatalf("restricted list = %+v, want only the restricted member", list.Participants)
	}
	banned, ok := list.Participants[0].(*tg.ChannelParticipantBanned)
	if !ok {
		t.Fatalf("restricted participant = %T, want channelParticipantBanned", list.Participants[0])
	}
	if banned.Left || !banned.BannedRights.SendMessages || banned.Peer.(*tg.PeerUser).UserID != fx.member.ID {
		t.Fatalf("restricted participant = %+v, want left=false send_messages for user %d", banned, fx.member.ID)
	}

	// 不按查看者裁剪：普通成员、受限者本人单查拿到的也是同一个类型。
	for _, viewer := range []domain.User{fx.other, fx.member} {
		if p, ok := fx.getParticipant(t, viewer, fx.member).(*tg.ChannelParticipantBanned); !ok || p.Left {
			t.Fatalf("restricted participant seen by %d = %#v, want channelParticipantBanned left=false", viewer.ID, fx.getParticipant(t, viewer, fx.member))
		}
	}
}

func TestAdminBotChatMemberUpdateForRestrictedMemberIsBanned(t *testing.T) {
	fx := newBannedParticipantFixture(t)
	bot := fx.newUser(t, 16504, true)
	if _, err := fx.channels.EditAdmin(fx.userCtx(fx.owner), fx.owner.ID, domain.EditChannelAdminRequest{
		ChannelID: fx.channel.ID, MemberID: bot.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: int(time.Now().Unix()),
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
	fx.editBanned(t, fx.member, tg.ChatBannedRights{SendMessages: true})

	var restricted *tg.UpdateChannelParticipant
	for _, p := range fx.pushedParticipants(bot.ID) {
		if p.UserID == fx.member.ID {
			restricted = p
		}
	}
	if restricted == nil {
		t.Fatalf("admin bot got no chat_member update for the restricted member: %+v", fx.pushedParticipants(bot.ID))
	}
	next, _ := restricted.GetNewParticipant()
	banned, ok := next.(*tg.ChannelParticipantBanned)
	if !ok {
		t.Fatalf("restricted new participant = %T, want channelParticipantBanned", next)
	}
	if banned.Left || !banned.BannedRights.SendMessages {
		t.Fatalf("restricted new participant = %+v, want left=false send_messages", banned)
	}
	if prev, _ := restricted.GetPrevParticipant(); prev == nil {
		t.Fatal("restricted update has no previous participant")
	} else if _, ok := prev.(*tg.ChannelParticipant); !ok {
		t.Fatalf("restricted previous participant = %T, want plain channelParticipant", prev)
	}
}
