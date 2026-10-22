package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	botsapp "telesrv/internal/app/bots"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// permissionWhitelist 返回总开关开启、发送白名单开启且名单为 ids 的权限设置。
func permissionWhitelist(userID int64, ids ...int64) domain.AccountPermissions {
	p := domain.DefaultAccountPermissions(userID)
	p.Enabled = true
	p.SendWhitelistEnabled = true
	p.SendWhitelist = ids
	return p
}

// TestPermissionSendWhitelistTargetsCounterpart 证明各发送入口把私聊/秘密聊天的对方交给了白名单
// 判定：对方不在名单里时返回 TELESRV_PERMISSION_SEND_WHITELIST，把对方加进名单后不再有权限错误。
func TestPermissionSendWhitelistTargetsCounterpart(t *testing.T) {
	f := newPermissionMappingFixture(t)
	stranger := domain.User{ID: f.other.ID + 1000}
	otherPeer := &tg.InputPeerUser{UserID: f.other.ID, AccessHash: f.other.AccessHash}
	secretPeer := tg.InputEncryptedChat{ChatID: f.chat.ID, AccessHash: f.chat.ParticipantAccessHash}
	cases := []struct {
		method string
		req    bin.Encoder
	}{
		{"messages.sendMessage", &tg.MessagesSendMessageRequest{Peer: otherPeer, Message: "hi", RandomID: 101}},
		{"messages.sendMedia", &tg.MessagesSendMediaRequest{Peer: otherPeer, Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 102}},
		{"messages.sendMedia webpage", &tg.MessagesSendMediaRequest{Peer: otherPeer, Media: &tg.InputMediaEmpty{}, Message: "x", RandomID: 103}},
		{"messages.sendMultiMedia", &tg.MessagesSendMultiMediaRequest{Peer: otherPeer, MultiMedia: []tg.InputSingleMedia{{Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 104}}}},
		{"messages.forwardMessages", &tg.MessagesForwardMessagesRequest{FromPeer: inputPeerChannel(f.group), ToPeer: otherPeer, ID: []int{1}, RandomID: []int64{105}}},
		{"messages.sendInlineBotResult", &tg.MessagesSendInlineBotResultRequest{Peer: otherPeer, QueryID: 1, ID: "r", RandomID: 106}},
		{"messages.sendScheduledMessages", &tg.MessagesSendScheduledMessagesRequest{Peer: otherPeer, ID: []int{1}}},
		{"messages.sendQuickReplyMessages", &tg.MessagesSendQuickReplyMessagesRequest{Peer: otherPeer, ShortcutID: 1, ID: []int{1}, RandomID: []int64{107}}},
		{"messages.requestEncryption", &tg.MessagesRequestEncryptionRequest{UserID: inputUser(f.other), RandomID: 108, GA: []byte{1}}},
		{"messages.acceptEncryption", &tg.MessagesAcceptEncryptionRequest{Peer: secretPeer, GB: []byte{1}, KeyFingerprint: 1}},
		{"messages.sendEncrypted", &tg.MessagesSendEncryptedRequest{Peer: secretPeer, RandomID: 109, Data: []byte{1}}},
		{"messages.sendEncryptedFile", &tg.MessagesSendEncryptedFileRequest{Peer: secretPeer, RandomID: 110, Data: []byte{1}, File: &tg.InputEncryptedFileEmpty{}}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID, stranger.ID)
			assertPermissionDenied(t, f.dispatch(t, tc.req), "TELESRV_PERMISSION_SEND_WHITELIST")

			f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID, f.other.ID)
			if err := f.dispatch(t, tc.req); isPermissionErr(err) {
				t.Fatalf("counterpart whitelisted: err = %v, want no permission error", err)
			}

			// 发送关闭优先于白名单：对方在名单里也返回 TELESRV_PERMISSION_SEND。
			p := permissionWhitelist(f.me.ID, f.other.ID)
			p.Flags.Send = false
			f.perms.items[f.me.ID] = p
			assertPermissionDenied(t, f.dispatch(t, tc.req), "TELESRV_PERMISSION_SEND")
		})
	}
}

// TestPermissionSendWhitelistIgnoresGroupsAndSavedMessages 证明目标是群组（超级群）或收藏夹时
// 不看白名单，即使名单为空。
func TestPermissionSendWhitelistIgnoresGroupsAndSavedMessages(t *testing.T) {
	f := newPermissionMappingFixture(t)
	f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID)
	groupPeer := inputPeerChannel(f.group)
	cases := []struct {
		name string
		req  bin.Encoder
	}{
		{"messages.sendMessage group", &tg.MessagesSendMessageRequest{Peer: groupPeer, Message: "hi", RandomID: 201}},
		{"messages.sendMedia group", &tg.MessagesSendMediaRequest{Peer: groupPeer, Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 202}},
		{"messages.forwardMessages to group", &tg.MessagesForwardMessagesRequest{FromPeer: &tg.InputPeerUser{UserID: f.other.ID, AccessHash: f.other.AccessHash}, ToPeer: groupPeer, ID: []int{1}, RandomID: []int64{203}}},
		{"messages.sendInlineBotResult group", &tg.MessagesSendInlineBotResultRequest{Peer: groupPeer, QueryID: 1, ID: "r", RandomID: 204}},
		{"messages.sendMessage saved messages", &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "note", RandomID: 205}},
		{"messages.forwardMessages to saved messages", &tg.MessagesForwardMessagesRequest{FromPeer: groupPeer, ToPeer: &tg.InputPeerSelf{}, ID: []int{1}, RandomID: []int64{206}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.dispatch(t, tc.req); isPermissionErr(err) {
				t.Fatalf("err = %v, want no permission error", err)
			}
		})
	}
}

// TestPermissionSendWhitelistAppliesToBotPrivateChats 证明和 bot 的私聊同样受白名单约束。
func TestPermissionSendWhitelistAppliesToBotPrivateChats(t *testing.T) {
	f := newPermissionMappingFixture(t)
	bot, err := f.users.Create(f.ctx, domain.User{AccessHash: 13, FirstName: "Helper", Username: "helper_bot", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	req := &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash}, Message: "/start", RandomID: 301}

	f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID, f.other.ID)
	assertPermissionDenied(t, f.dispatch(t, req), "TELESRV_PERMISSION_SEND_WHITELIST")

	f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID, f.other.ID, bot.ID)
	if err := f.dispatch(t, req); isPermissionErr(err) {
		t.Fatalf("bot whitelisted: err = %v, want no permission error", err)
	}
}

// TestPermissionBotMessageEntriesCountAsSends 证明三个会以用户身份给 bot 发出消息的入口按发送
// 判定、目标是那个 bot：messages.startBot（/start）、messages.sendWebViewData（mini app 回传）、
// messages.sendBotRequestedPeer（把选中的会话发给 bot）。名单外的 bot 返回
// TELESRV_PERMISSION_SEND_WHITELIST，发送关闭时返回 TELESRV_PERMISSION_SEND。
func TestPermissionBotMessageEntriesCountAsSends(t *testing.T) {
	f := newPermissionMappingFixture(t)
	dialogs := memory.NewDialogStore()
	f.router.deps.Bots = botsapp.NewService(f.users, memory.NewBotStore(f.users), memory.NewMessageStore(dialogs))
	bot, err := f.users.Create(f.ctx, domain.User{AccessHash: 17, FirstName: "Shop", Username: "shop_bot", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botPeer := &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash}
	botUser := &tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash}
	cases := []struct {
		method string
		req    bin.Encoder
	}{
		{"messages.startBot", &tg.MessagesStartBotRequest{Bot: botUser, Peer: botPeer, RandomID: 401, StartParam: "ref"}},
		{"messages.sendWebViewData", &tg.MessagesSendWebViewDataRequest{Bot: botUser, RandomID: 402, ButtonText: "Pay", Data: "{}"}},
		{"messages.sendBotRequestedPeer", &tg.MessagesSendBotRequestedPeerRequest{Peer: botPeer, MsgID: 1, ButtonID: 1, RequestedPeers: []tg.InputPeerClass{&tg.InputPeerSelf{}}}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID, f.other.ID)
			assertPermissionDenied(t, f.dispatch(t, tc.req), "TELESRV_PERMISSION_SEND_WHITELIST")

			f.perms.items[f.me.ID] = permissionWhitelist(f.me.ID, bot.ID)
			if err := f.dispatch(t, tc.req); isPermissionErr(err) {
				t.Fatalf("bot whitelisted: err = %v, want no permission error", err)
			}

			p := permissionWhitelist(f.me.ID, bot.ID)
			p.Flags.Send = false
			f.perms.items[f.me.ID] = p
			assertPermissionDenied(t, f.dispatch(t, tc.req), "TELESRV_PERMISSION_SEND")
		})
	}
}
