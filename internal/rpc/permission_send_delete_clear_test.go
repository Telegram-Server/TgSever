package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// permissionSecretChats 只实现 GetSecretChat：权限判定据此找出秘密聊天的对方，其余方法
// 不应在被拒绝的请求里被调用（调用即 panic，被 Router 的 panic 隔离转成 INTERNAL）。
type permissionSecretChats struct {
	SecretChatService
	chat domain.SecretChat
}

func (s permissionSecretChats) GetSecretChat(_ context.Context, chatID int) (domain.SecretChat, bool, error) {
	if chatID != s.chat.ID {
		return domain.SecretChat{}, false, nil
	}
	return s.chat, true, nil
}

// permissionFlagsOff 返回总开关开启、只关掉 off 里各项的权限设置。
func permissionFlagsOff(userID int64, off ...string) domain.AccountPermissions {
	p := domain.DefaultAccountPermissions(userID)
	p.Enabled = true
	for _, item := range off {
		switch item {
		case "send":
			p.Flags.Send = false
		case "delete":
			p.Flags.Delete = false
		case "clear":
			p.Flags.ClearHistory = false
		default:
			panic("unknown permission item " + item)
		}
	}
	return p
}

type permissionMappingFixture struct {
	router *Router
	perms  *fakePermissionsService
	ctx    context.Context
	me     domain.User
	other  domain.User
	group  *tg.Channel
	chat   domain.SecretChat
	users  *memory.UserStore
}

func newPermissionMappingFixture(t *testing.T) *permissionMappingFixture {
	t.Helper()
	f := newRPCChannelFixture(t)
	me := f.user(11, "15550120001", "Staff")
	other := f.user(12, "15550120002", "Friend")
	group := f.createLegacyMegagroup(me, "Work", other)
	authKey := [8]byte{1}
	chat := domain.SecretChat{
		ID:                    777,
		AdminUserID:           other.ID,
		ParticipantUserID:     me.ID,
		AdminAccessHash:       71,
		ParticipantAccessHash: 72,
		ParticipantAuthKeyID:  businessAuthKeyInt64(authKey),
		State:                 domain.SecretChatStateNormal,
	}
	perms := &fakePermissionsService{items: map[int64]domain.AccountPermissions{}}
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:       appusers.NewService(f.users),
		Channels:    appchannels.NewService(f.channels),
		SecretChats: permissionSecretChats{chat: chat},
		Permissions: perms,
		// 业务自动化可用，sendQuickReplyMessages 才会走到发送权限判定。
		Account:  &permissionQuickReplies{},
		Messages: permissionNoMessages{},
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithAuthKeyID(WithUserID(context.Background(), me.ID), authKey)
	return &permissionMappingFixture{router: router, perms: perms, ctx: ctx, me: me, other: other, group: group, chat: chat, users: f.users}
}

func (f *permissionMappingFixture) dispatch(t *testing.T, req bin.Encoder) error {
	t.Helper()
	var body bin.Buffer
	if err := req.Encode(&body); err != nil {
		t.Fatalf("encode %T: %v", req, err)
	}
	_, err := f.router.Dispatch(f.ctx, [8]byte{1}, 10, &body)
	return err
}

func isPermissionErr(err error) bool {
	rpcErr, ok := tgerr.As(err)
	return ok && strings.HasPrefix(rpcErr.Type, "TELESRV_PERMISSION_")
}

func TestPermissionSendDeleteClearMethodMapping(t *testing.T) {
	f := newPermissionMappingFixture(t)
	otherPeer := &tg.InputPeerUser{UserID: f.other.ID, AccessHash: f.other.AccessHash}
	groupPeer := inputPeerChannel(f.group)
	secretPeer := tg.InputEncryptedChat{ChatID: f.chat.ID, AccessHash: f.chat.ParticipantAccessHash}
	cases := []struct {
		method string
		item   string
		denial string
		req    bin.Encoder
	}{
		{"messages.sendMessage private", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendMessageRequest{Peer: otherPeer, Message: "hi", RandomID: 1}},
		{"messages.sendMessage group", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendMessageRequest{Peer: groupPeer, Message: "hi", RandomID: 2}},
		{"messages.sendMedia", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendMediaRequest{Peer: otherPeer, Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 3}},
		{"messages.sendMedia webpage", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendMediaRequest{Peer: otherPeer, Media: &tg.InputMediaEmpty{}, Message: "x", RandomID: 4}},
		{"messages.sendMultiMedia", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendMultiMediaRequest{Peer: otherPeer, MultiMedia: []tg.InputSingleMedia{{Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 5}}}},
		{"messages.forwardMessages", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesForwardMessagesRequest{FromPeer: &tg.InputPeerSelf{}, ToPeer: otherPeer, ID: []int{1}, RandomID: []int64{6}}},
		{"messages.sendInlineBotResult", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendInlineBotResultRequest{Peer: otherPeer, QueryID: 1, ID: "r", RandomID: 7}},
		{"messages.sendScheduledMessages", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendScheduledMessagesRequest{Peer: otherPeer, ID: []int{1}}},
		{"messages.sendQuickReplyMessages", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendQuickReplyMessagesRequest{Peer: otherPeer, ShortcutID: 1, ID: []int{1}, RandomID: []int64{8}}},
		{"messages.requestEncryption", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesRequestEncryptionRequest{UserID: inputUser(f.other), RandomID: 9, GA: []byte{1}}},
		{"messages.acceptEncryption", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesAcceptEncryptionRequest{Peer: secretPeer, GB: []byte{1}, KeyFingerprint: 1}},
		{"messages.sendEncrypted", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendEncryptedRequest{Peer: secretPeer, RandomID: 10, Data: []byte{1}}},
		{"messages.sendEncryptedFile", "send", "TELESRV_PERMISSION_SEND", &tg.MessagesSendEncryptedFileRequest{Peer: secretPeer, RandomID: 11, Data: []byte{1}, File: &tg.InputEncryptedFileEmpty{}}},
		{"messages.deleteMessages", "delete", "TELESRV_PERMISSION_DELETE", &tg.MessagesDeleteMessagesRequest{ID: []int{1}}},
		{"channels.deleteMessages", "delete", "TELESRV_PERMISSION_DELETE", &tg.ChannelsDeleteMessagesRequest{Channel: inputChannel(f.group), ID: []int{1}}},
		{"channels.deleteParticipantHistory", "delete", "TELESRV_PERMISSION_DELETE", &tg.ChannelsDeleteParticipantHistoryRequest{Channel: inputChannel(f.group), Participant: otherPeer}},
		{"messages.setHistoryTTL on", "delete", "TELESRV_PERMISSION_DELETE", &tg.MessagesSetHistoryTTLRequest{Peer: otherPeer, Period: 86400}},
		{"messages.setDefaultHistoryTTL on", "delete", "TELESRV_PERMISSION_DELETE", &tg.MessagesSetDefaultHistoryTTLRequest{Period: 86400}},
		{"messages.deleteHistory private", "clear", "TELESRV_PERMISSION_CLEAR_HISTORY", &tg.MessagesDeleteHistoryRequest{Peer: otherPeer}},
		{"messages.deleteHistory saved messages", "clear", "TELESRV_PERMISSION_CLEAR_HISTORY", &tg.MessagesDeleteHistoryRequest{Peer: &tg.InputPeerSelf{}}},
		{"messages.deleteSavedHistory", "clear", "TELESRV_PERMISSION_CLEAR_HISTORY", &tg.MessagesDeleteSavedHistoryRequest{Peer: otherPeer}},
	}
	items := []string{"send", "delete", "clear"}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			f.perms.items[f.me.ID] = permissionFlagsOff(f.me.ID, tc.item)
			err := f.dispatch(t, tc.req)
			assertPermissionDenied(t, err, tc.denial)

			// 只有这一项允许、另外两项关闭：这一项的方法不受影响。
			var others []string
			for _, item := range items {
				if item != tc.item {
					others = append(others, item)
				}
			}
			f.perms.items[f.me.ID] = permissionFlagsOff(f.me.ID, others...)
			if err := f.dispatch(t, tc.req); isPermissionErr(err) {
				t.Fatalf("with %s allowed: err = %v, want no permission error", tc.item, err)
			}
		})
	}
}

func TestPermissionSendDeleteClearLeaveOtherMethodsAlone(t *testing.T) {
	f := newPermissionMappingFixture(t)
	f.perms.items[f.me.ID] = permissionFlagsOff(f.me.ID, "send", "delete", "clear")
	otherPeer := &tg.InputPeerUser{UserID: f.other.ID, AccessHash: f.other.AccessHash}
	groupPeer := inputPeerChannel(f.group)
	secretPeer := tg.InputEncryptedChat{ChatID: f.chat.ID, AccessHash: f.chat.ParticipantAccessHash}
	cases := []struct {
		name string
		req  bin.Encoder
	}{
		{"messages.getHistory", &tg.MessagesGetHistoryRequest{Peer: otherPeer, Limit: 10}},
		{"messages.getMessages", &tg.MessagesGetMessagesRequest{ID: []tg.InputMessageClass{&tg.InputMessageID{ID: 1}}}},
		{"messages.sendEncryptedService", &tg.MessagesSendEncryptedServiceRequest{Peer: secretPeer, RandomID: 1, Data: []byte{1}}},
		{"messages.setHistoryTTL off", &tg.MessagesSetHistoryTTLRequest{Peer: otherPeer, Period: 0}},
		{"messages.setDefaultHistoryTTL off", &tg.MessagesSetDefaultHistoryTTLRequest{Period: 0}},
		{"messages.deleteScheduledMessages", &tg.MessagesDeleteScheduledMessagesRequest{Peer: otherPeer, ID: []int{1}}},
		// 清空对话只约束私聊与收藏夹：群组的清空会被退出普通群、升级为超级群连带调用。
		{"channels.deleteHistory group", &tg.ChannelsDeleteHistoryRequest{Channel: inputChannel(f.group)}},
		{"messages.deleteHistory group", &tg.MessagesDeleteHistoryRequest{Peer: groupPeer}},
		{"messages.deleteHistory legacy chat", &tg.MessagesDeleteHistoryRequest{Peer: &tg.InputPeerChat{ChatID: f.group.ID}}},
		{"messages.sendMessage to saved messages", &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "note", RandomID: 21}},
		{"messages.sendMedia to saved messages", &tg.MessagesSendMediaRequest{Peer: &tg.InputPeerSelf{}, Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 22}},
		{"messages.sendMultiMedia to saved messages", &tg.MessagesSendMultiMediaRequest{Peer: &tg.InputPeerSelf{}, MultiMedia: []tg.InputSingleMedia{{Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: 23}}}},
		{"messages.forwardMessages to saved messages", &tg.MessagesForwardMessagesRequest{FromPeer: otherPeer, ToPeer: &tg.InputPeerSelf{}, ID: []int{1}, RandomID: []int64{24}}},
		{"messages.sendScheduledMessages to saved messages", &tg.MessagesSendScheduledMessagesRequest{Peer: &tg.InputPeerSelf{}, ID: []int{1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.dispatch(t, tc.req); isPermissionErr(err) {
				t.Fatalf("err = %v, want no permission error", err)
			}
		})
	}
	// sendScreenshotNotification 尚未实现，按方法名登记在闸门里，实现后自动受控。
	assertPermissionDenied(t, f.router.checkPermissionRPC(f.ctx, "messages.sendScreenshotNotification"), "TELESRV_PERMISSION_SEND")
}

func TestPermissionSendOffStillWritesSavedMessages(t *testing.T) {
	f := newScheduledFixture(t)
	f.router.deps.Permissions = &fakePermissionsService{items: map[int64]domain.AccountPermissions{
		f.owner.ID: permissionFlagsOff(f.owner.ID, "send"),
	}}
	if _, err := f.router.onMessagesSendMessage(f.ctx(f.owner), &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "note", RandomID: 31}); err != nil {
		t.Fatalf("send to saved messages: %v", err)
	}
	if _, ok := f.historyByRandomID(domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, 31); !ok {
		t.Fatal("saved message not in history")
	}
	_, err := f.router.onMessagesSendMessage(f.ctx(f.owner), &tg.MessagesSendMessageRequest{Peer: f.friendPeer(), Message: "hi", RandomID: 32})
	assertPermissionDenied(t, err, "TELESRV_PERMISSION_SEND")
	if _, ok := f.privateByRandomID(32); ok {
		t.Fatal("denied message reached history")
	}
}

func TestPermissionSendOffDoesNotStopQueuedScheduledMessages(t *testing.T) {
	f := newScheduledFixture(t)
	if _, err := f.router.onMessagesSendMessage(f.ctx(f.owner), &tg.MessagesSendMessageRequest{
		Peer: f.friendPeer(), Message: "later", RandomID: 41, ScheduleDate: f.scheduleDate(),
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	f.router.deps.Permissions = &fakePermissionsService{items: map[int64]domain.AccountPermissions{
		f.owner.ID: permissionFlagsOff(f.owner.ID, "send"),
	}}
	if !f.dispatch() {
		t.Fatal("dispatcher claimed nothing")
	}
	if _, ok := f.privateByRandomID(41); !ok {
		t.Fatal("queued scheduled message was not delivered after send was turned off")
	}
}
