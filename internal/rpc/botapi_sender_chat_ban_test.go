package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// senderChatBotAPIID 是频道的 Bot API chat_id（-100 前缀）。
func senderChatBotAPIID(ch domain.Channel) int64 {
	return -botAPIChannelChatIDBase - ch.ID
}

func (f botAPIBridgeFixture) senderChatBanned(t *testing.T, senderChatID int64) bool {
	t.Helper()
	ok, err := f.channels.IsSenderChatBanned(f.ctx, f.channel.ID, senderChatID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// kickedList 以群主身份读取 kicked 筛选。
func (f botAPIBridgeFixture) kickedList(t *testing.T) *tg.ChannelsChannelParticipants {
	t.Helper()
	res, err := f.router.onChannelsGetParticipants(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: f.inputChannel(), Filter: &tg.ChannelParticipantsKicked{}, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*tg.ChannelsChannelParticipants)
}

func TestBotAPIBanChatSenderChatRequiresBanRight(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sender := f.senderChannel(t, "Spam Channel")

	wantBotAPIError(t, f.router.BotAPIBanChatSenderChat(f.ctx, f.bot.ID, f.chatID, senderChatBotAPIID(sender)), "CHAT_ADMIN_REQUIRED")
	if f.senderChatBanned(t, sender.ID) {
		t.Fatal("bot without ban right banned the channel")
	}

	// 解封同样需要封禁权限：群主先封，普通成员 bot 解不开。
	if err := f.editBannedChannelPeer(f.owner.ID, inputPeerOfChannel(sender), tg.ChatBannedRights{ViewMessages: true}); err != nil {
		t.Fatal(err)
	}
	wantBotAPIError(t, f.router.BotAPIUnbanChatSenderChat(f.ctx, f.bot.ID, f.chatID, senderChatBotAPIID(sender)), "CHAT_ADMIN_REQUIRED")
	if !f.senderChatBanned(t, sender.ID) {
		t.Fatal("bot without ban right unbanned the channel")
	}
}

func TestBotAPIBanChatSenderChatBansAndUnbans(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sender := f.senderChannel(t, "Spam Channel")
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true})

	if err := f.router.BotAPIBanChatSenderChat(f.ctx, f.bot.ID, f.chatID, senderChatBotAPIID(sender)); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if !f.senderChatBanned(t, sender.ID) {
		t.Fatal("channel not banned")
	}
	// 重复封禁幂等。
	if err := f.router.BotAPIBanChatSenderChat(f.ctx, f.bot.ID, f.chatID, senderChatBotAPIID(sender)); err != nil {
		t.Fatalf("repeat ban: %v", err)
	}

	list := f.kickedList(t)
	if list.Count != 1 || len(list.Participants) != 1 {
		t.Fatalf("kicked list = %+v", list)
	}
	banned, ok := list.Participants[0].(*tg.ChannelParticipantBanned)
	if !ok {
		t.Fatalf("kicked participant = %T, want channelParticipantBanned", list.Participants[0])
	}
	if p, ok := banned.Peer.(*tg.PeerChannel); !ok || p.ChannelID != sender.ID || banned.KickedBy != f.bot.ID || !banned.BannedRights.ViewMessages {
		t.Fatalf("kicked participant = %+v", banned)
	}

	if err := f.router.BotAPIUnbanChatSenderChat(f.ctx, f.bot.ID, f.chatID, senderChatBotAPIID(sender)); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if f.senderChatBanned(t, sender.ID) {
		t.Fatal("channel still banned after unban")
	}
	if list := f.kickedList(t); list.Count != 0 || len(list.Participants) != 0 {
		t.Fatalf("kicked after unban = %+v", list)
	}
	// 重复解封幂等。
	if err := f.router.BotAPIUnbanChatSenderChat(f.ctx, f.bot.ID, f.chatID, senderChatBotAPIID(sender)); err != nil {
		t.Fatalf("repeat unban: %v", err)
	}
}

// sender_chat_id 不是频道、频道不存在或就是本群时返回参数错误。
func TestBotAPIBanChatSenderChatRejectsInvalidSenderChat(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true})

	for _, tc := range []struct {
		name         string
		senderChatID int64
		want         string
	}{
		{name: "user id", senderChatID: f.member.ID, want: "SENDER_CHAT_ID_INVALID"},
		{name: "zero", senderChatID: 0, want: "SENDER_CHAT_ID_INVALID"},
		{name: "unknown channel", senderChatID: -botAPIChannelChatIDBase - 987654321, want: "PEER_ID_INVALID"},
		{name: "the chat itself", senderChatID: f.chatID, want: "PEER_ID_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantBotAPIError(t, f.router.BotAPIBanChatSenderChat(f.ctx, f.bot.ID, f.chatID, tc.senderChatID), tc.want)
			wantBotAPIError(t, f.router.BotAPIUnbanChatSenderChat(f.ctx, f.bot.ID, f.chatID, tc.senderChatID), tc.want)
		})
	}
	if list := f.kickedList(t); list.Count != 0 {
		t.Fatalf("kicked list after rejected requests = %+v", list)
	}
}
