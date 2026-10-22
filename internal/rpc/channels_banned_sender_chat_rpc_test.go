package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// senderChannel 让成员 member 建一个广播频道，作为要被封禁的发言频道。
func (f botAPIBridgeFixture) senderChannel(t *testing.T, title string) domain.Channel {
	t.Helper()
	res, err := f.channels.CreateChannel(f.ctx, f.member.ID, domain.CreateChannelRequest{Title: title, Broadcast: true, Date: 20})
	if err != nil {
		t.Fatalf("create sender channel: %v", err)
	}
	return res.Channel
}

func (f botAPIBridgeFixture) editBannedChannelPeer(by int64, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	_, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, by), &tg.ChannelsEditBannedRequest{
		Channel:      f.inputChannel(),
		Participant:  peer,
		BannedRights: rights,
	})
	return err
}

func inputPeerOfChannel(ch domain.Channel) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
}

func TestChannelsEditBannedAcceptsChannelPeer(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sender := f.senderChannel(t, "Spam Channel")
	peer := inputPeerOfChannel(sender)
	banned := func() bool {
		t.Helper()
		ok, err := f.channels.IsSenderChatBanned(f.ctx, f.channel.ID, sender.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if err := f.editBannedChannelPeer(f.member.ID, peer, tg.ChatBannedRights{ViewMessages: true}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member bans channel = %v, want CHAT_ADMIN_REQUIRED", err)
	}
	if err := f.editBannedChannelPeer(f.owner.ID, peer, tg.ChatBannedRights{SendMessages: true}); !tgerr.Is(err, "BANNED_RIGHTS_INVALID") {
		t.Fatalf("partial channel ban = %v, want BANNED_RIGHTS_INVALID", err)
	}
	if err := f.editBannedChannelPeer(f.owner.ID, &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}, tg.ChatBannedRights{ViewMessages: true}); !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("ban the group itself = %v, want PEER_ID_INVALID", err)
	}
	if banned() {
		t.Fatal("rejected requests recorded a ban")
	}

	// tdesktop 封禁发言频道时带上全部禁止位与最大期限。
	full := tg.ChatBannedRights{ViewMessages: true, SendMessages: true, SendMedia: true, UntilDate: 2147483647}
	if err := f.editBannedChannelPeer(f.owner.ID, peer, full); err != nil {
		t.Fatalf("owner bans channel: %v", err)
	}
	if !banned() {
		t.Fatal("channel not banned")
	}
	if err := f.editBannedChannelPeer(f.owner.ID, peer, full); err != nil {
		t.Fatalf("repeat ban: %v", err)
	}
	if err := f.editBannedChannelPeer(f.owner.ID, peer, tg.ChatBannedRights{}); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if banned() {
		t.Fatal("channel still banned after unban")
	}
}

func TestChannelsEditBannedChannelPeerByBotAdmin(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sender := f.senderChannel(t, "Spam Channel")
	if err := f.editBannedChannelPeer(f.bot.ID, inputPeerOfChannel(sender), tg.ChatBannedRights{ViewMessages: true}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("plain bot bans channel = %v, want CHAT_ADMIN_REQUIRED", err)
	}
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true})
	if err := f.editBannedChannelPeer(f.bot.ID, inputPeerOfChannel(sender), tg.ChatBannedRights{ViewMessages: true}); err != nil {
		t.Fatalf("bot admin bans channel: %v", err)
	}
	if ok, err := f.channels.IsSenderChatBanned(f.ctx, f.channel.ID, sender.ID); err != nil || !ok {
		t.Fatalf("banned = %v, %v", ok, err)
	}
}

func TestChannelsBannedChannelPeerProjections(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sender := f.senderChannel(t, "Spam Channel")
	peer := inputPeerOfChannel(sender)
	if err := f.editBannedChannelPeer(f.owner.ID, peer, tg.ChatBannedRights{ViewMessages: true}); err != nil {
		t.Fatal(err)
	}
	ownerCtx := WithUserID(f.ctx, f.owner.ID)
	assertBannedChannel := func(where string, participant tg.ChannelParticipantClass) {
		t.Helper()
		banned, ok := participant.(*tg.ChannelParticipantBanned)
		if !ok {
			t.Fatalf("%s participant = %T, want channelParticipantBanned", where, participant)
		}
		if p, ok := banned.Peer.(*tg.PeerChannel); !ok || p.ChannelID != sender.ID || !banned.Left || banned.KickedBy != f.owner.ID || !banned.BannedRights.ViewMessages {
			t.Fatalf("%s participant = %+v", where, banned)
		}
	}
	hasChat := func(where string, chats []tg.ChatClass) {
		t.Helper()
		for _, chat := range chats {
			if ch, ok := chat.(*tg.Channel); ok && ch.ID == sender.ID {
				return
			}
		}
		t.Fatalf("%s chats %v miss sender channel %d", where, chats, sender.ID)
	}

	res, err := f.router.onChannelsGetParticipants(ownerCtx, &tg.ChannelsGetParticipantsRequest{
		Channel: f.inputChannel(), Filter: &tg.ChannelParticipantsKicked{}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	list := res.(*tg.ChannelsChannelParticipants)
	if list.Count != 1 || len(list.Participants) != 1 {
		t.Fatalf("kicked list = %+v", list)
	}
	assertBannedChannel("kicked", list.Participants[0])
	hasChat("kicked", list.Chats)

	one, err := f.router.onChannelsGetParticipant(ownerCtx, &tg.ChannelsGetParticipantRequest{Channel: f.inputChannel(), Participant: peer})
	if err != nil {
		t.Fatal(err)
	}
	assertBannedChannel("getParticipant", one.Participant)
	hasChat("getParticipant", one.Chats)

	if err := f.editBannedChannelPeer(f.owner.ID, peer, tg.ChatBannedRights{}); err != nil {
		t.Fatal(err)
	}
	logRes, err := f.router.onChannelsGetAdminLog(ownerCtx, &tg.ChannelsGetAdminLogRequest{Channel: f.inputChannel(), Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var toggles []*tg.ChannelAdminLogEventActionParticipantToggleBan
	for _, event := range logRes.Events {
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionParticipantToggleBan); ok {
			toggles = append(toggles, action)
		}
	}
	if len(toggles) != 2 {
		t.Fatalf("toggle ban events = %d, want ban + unban", len(toggles))
	}
	assertBannedChannel("unban log prev", toggles[0].PrevParticipant)
	if left, ok := toggles[0].NewParticipant.(*tg.ChannelParticipantLeft); !ok || left.Peer.(*tg.PeerChannel).ChannelID != sender.ID {
		t.Fatalf("unban log new = %#v, want channelParticipantLeft for the channel", toggles[0].NewParticipant)
	}
	assertBannedChannel("ban log new", toggles[1].NewParticipant)
	hasChat("admin log", logRes.Chats)

	res, err = f.router.onChannelsGetParticipants(ownerCtx, &tg.ChannelsGetParticipantsRequest{
		Channel: f.inputChannel(), Filter: &tg.ChannelParticipantsKicked{}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if list := res.(*tg.ChannelsChannelParticipants); list.Count != 0 || len(list.Participants) != 0 {
		t.Fatalf("kicked after unban = %+v", list)
	}
	if _, err := f.router.onChannelsGetParticipant(ownerCtx, &tg.ChannelsGetParticipantRequest{Channel: f.inputChannel(), Participant: peer}); !tgerr.Is(err, "USER_NOT_PARTICIPANT") {
		t.Fatalf("getParticipant after unban = %v, want USER_NOT_PARTICIPANT", err)
	}
}
