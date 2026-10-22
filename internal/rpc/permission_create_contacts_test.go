package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func permissionGateFlags(userID int64, mutate func(*domain.AccountPermissionFlags)) *fakePermissionsService {
	p := domain.DefaultAccountPermissions(userID)
	p.Enabled = true
	mutate(&p.Flags)
	return &fakePermissionsService{items: map[int64]domain.AccountPermissions{userID: p}}
}

func TestPermissionGateCreateChatAndContactMethods(t *testing.T) {
	const userID = int64(1001)
	ctx := WithUserID(context.Background(), userID)
	allOff := &Router{deps: Deps{Permissions: permissionGateFlags(userID, func(f *domain.AccountPermissionFlags) {
		*f = domain.AccountPermissionFlags{}
	})}}
	for method, want := range map[string]string{
		"messages.createChat":         "TELESRV_PERMISSION_CREATE_GROUP",
		"contacts.addContact":         "TELESRV_PERMISSION_ADD_CONTACT",
		"contacts.importContacts":     "TELESRV_PERMISSION_ADD_CONTACT",
		"contacts.acceptContact":      "TELESRV_PERMISSION_ADD_CONTACT",
		"contacts.importContactToken": "TELESRV_PERMISSION_ADD_CONTACT",
	} {
		assertPermissionDenied(t, allOff.checkPermissionRPC(ctx, method), want)
	}
	// 删除与读取联系人不受限；channels.createChannel 按 flag 在 handler 里判定。
	for _, method := range []string{
		"contacts.deleteContacts", "contacts.deleteByPhones", "contacts.getContacts",
		"contacts.getContactIDs", "contacts.resolveUsername", "contacts.search",
		"channels.createChannel",
	} {
		if err := allOff.checkPermissionRPC(ctx, method); err != nil {
			t.Fatalf("%s gated: %v", method, err)
		}
	}

	onlyChannelOff := &Router{deps: Deps{Permissions: permissionGateFlags(userID, func(f *domain.AccountPermissionFlags) {
		f.CreateChannel = false
	})}}
	for _, method := range []string{"messages.createChat", "contacts.addContact"} {
		if err := onlyChannelOff.checkPermissionRPC(ctx, method); err != nil {
			t.Fatalf("%s gated with only create channel off: %v", method, err)
		}
	}
}

func TestPermissionGateDeniesCreateChatBeforeLayerHandler(t *testing.T) {
	const userID = int64(1001)
	perms := permissionGateFlags(userID, func(f *domain.AccountPermissionFlags) { f.CreateGroup = false })
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Permissions: perms}, zaptest.NewLogger(t), clock.System)
	body := encodeExactLayerRPC(t, tlprofile.Profile228, &tg.MessagesCreateChatRequest{Users: []tg.InputUserClass{}, Title: "must not reach handler"})
	admitted, err := router.AdmitLayer(tlprofile.Profile228, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, method, err := router.DispatchAdmitted(WithUserID(context.Background(), userID), [8]byte{1}, 10, 0, 1, admitted)
	if method != "messages.createChat" {
		t.Fatalf("method = %q", method)
	}
	assertPermissionDenied(t, err, "TELESRV_PERMISSION_CREATE_GROUP")
}

func TestPermissionCreateChannelDecidedByFlags(t *testing.T) {
	groupOff := func(f *domain.AccountPermissionFlags) { f.CreateGroup = false }
	channelOff := func(f *domain.AccountPermissionFlags) { f.CreateChannel = false }
	cases := []struct {
		name   string
		mutate func(*domain.AccountPermissionFlags)
		req    tg.ChannelsCreateChannelRequest
		want   string
	}{
		{"group off denies megagroup", groupOff, tg.ChannelsCreateChannelRequest{Megagroup: true}, "TELESRV_PERMISSION_CREATE_GROUP"},
		{"group off denies forum", groupOff, tg.ChannelsCreateChannelRequest{Megagroup: true, Forum: true}, "TELESRV_PERMISSION_CREATE_GROUP"},
		{"group off allows broadcast", groupOff, tg.ChannelsCreateChannelRequest{Broadcast: true}, ""},
		{"channel off denies broadcast", channelOff, tg.ChannelsCreateChannelRequest{Broadcast: true}, "TELESRV_PERMISSION_CREATE_CHANNEL"},
		{"channel off denies flagless request (created as broadcast)", channelOff, tg.ChannelsCreateChannelRequest{}, "TELESRV_PERMISSION_CREATE_CHANNEL"},
		{"channel off allows megagroup", channelOff, tg.ChannelsCreateChannelRequest{Megagroup: true}, ""},
		{"channel off allows forum", channelOff, tg.ChannelsCreateChannelRequest{Megagroup: true, Forum: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRPCChannelFixture(t)
			owner := f.user(1, "15550001001", "Owner")
			f.router.deps.Permissions = permissionGateFlags(owner.ID, tc.mutate)
			req := tc.req
			req.Title = "Work"
			_, err := f.router.onChannelsCreateChannel(f.userCtx(owner), &req)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				return
			}
			assertPermissionDenied(t, err, tc.want)
		})
	}
}
