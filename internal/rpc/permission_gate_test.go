package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

type fakePermissionsService struct {
	items       map[int64]domain.AccountPermissions
	err         error
	invalidated []int64
}

func (s *fakePermissionsService) View(_ context.Context, userID int64) (permissions.View, error) {
	if s.err != nil {
		return permissions.View{}, s.err
	}
	return permissions.NewView(s.items[userID], false), nil
}

func (s *fakePermissionsService) InvalidateAccountPermissions(userID int64) {
	s.invalidated = append(s.invalidated, userID)
}

func permissionGateEditDenied(userID int64) *fakePermissionsService {
	p := domain.DefaultAccountPermissions(userID)
	p.Enabled = true
	p.Flags.Edit = false
	return &fakePermissionsService{items: map[int64]domain.AccountPermissions{userID: p}}
}

func assertPermissionDenied(t *testing.T, err error, name string) {
	t.Helper()
	rpcErr, ok := tgerr.As(err)
	if !ok || rpcErr.Code != 403 || rpcErr.Type != name {
		t.Fatalf("err = %v, want 403 %s", err, name)
	}
}

func TestPermissionGateDeniesEditMessageBeforeLayerHandler(t *testing.T) {
	const userID = int64(1001)
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Permissions: permissionGateEditDenied(userID)}, zaptest.NewLogger(t), clock.System)
	body := encodeExactLayerRPC(t, tlprofile.Profile228, &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerSelf{}, ID: 1, Message: "must not reach handler"})
	admitted, err := router.AdmitLayer(tlprofile.Profile228, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, method, err := router.DispatchAdmitted(WithUserID(context.Background(), userID), [8]byte{1}, 10, 0, 1, admitted)
	if method != "messages.editMessage" {
		t.Fatalf("method = %q", method)
	}
	assertPermissionDenied(t, err, "TELESRV_PERMISSION_EDIT")
}

func TestPermissionGateDeniesEditMessageBeforeLegacyHandler(t *testing.T) {
	const userID = int64(1001)
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Permissions: permissionGateEditDenied(userID)}, zaptest.NewLogger(t), clock.System)
	var body bin.Buffer
	if err := (&tg.MessagesEditMessageRequest{Peer: &tg.InputPeerSelf{}, ID: 1, Message: "x"}).Encode(&body); err != nil {
		t.Fatal(err)
	}
	_, err := router.Dispatch(WithUserID(context.Background(), userID), [8]byte{1}, 10, &body)
	assertPermissionDenied(t, err, "TELESRV_PERMISSION_EDIT")
}

func TestPermissionGateMethodMapping(t *testing.T) {
	const userID = int64(1001)
	ctx := WithUserID(context.Background(), userID)
	denied := &Router{deps: Deps{Permissions: permissionGateEditDenied(userID)}}
	assertPermissionDenied(t, denied.checkPermissionRPC(ctx, "messages.editMessage"), "TELESRV_PERMISSION_EDIT")
	for _, method := range []string{"messages.getHistory", "messages.getMessages", "help.getAppConfig", "messages.sendMessage"} {
		if err := denied.checkPermissionRPC(ctx, method); err != nil {
			t.Fatalf("%s gated: %v", method, err)
		}
	}
	if err := denied.checkPermissionRPC(WithUserID(context.Background(), userID+1), "messages.editMessage"); err != nil {
		t.Fatalf("account without a record gated: %v", err)
	}
	if err := denied.checkPermissionRPC(context.Background(), "messages.editMessage"); err != nil {
		t.Fatalf("unauthorized context gated: %v", err)
	}
	if err := (&Router{}).checkPermissionRPC(ctx, "messages.editMessage"); err != nil {
		t.Fatalf("router without permissions gated: %v", err)
	}
}

func TestPermissionGateFailsClosedWhenViewUnavailable(t *testing.T) {
	router := &Router{deps: Deps{Permissions: &fakePermissionsService{err: errors.New("db down")}}}
	err := router.checkPermissionRPC(WithUserID(context.Background(), 1001), "messages.editMessage")
	if rpcErr, ok := tgerr.As(err); !ok || rpcErr.Code != 500 {
		t.Fatalf("err = %v, want internal error", err)
	}
}

func TestNotifyAccountPermissionsChangedPushesUpdateConfig(t *testing.T) {
	const userID = int64(1001)
	perms := &fakePermissionsService{}
	sessions := &captureSessions{}
	router := New(Config{}, Deps{Sessions: sessions, Permissions: perms}, zaptest.NewLogger(t), clock.System)
	if err := router.NotifyAccountPermissionsChanged(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	if got := sessions.pushedUserIDs(); len(got) != 1 || got[0] != userID {
		t.Fatalf("pushed users = %v, want [%d]", got, userID)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed = %#v, want one update", sessions.lastUserPush())
	}
	if _, ok := updates.Updates[0].(*tg.UpdateConfig); !ok {
		t.Fatalf("pushed update = %#v, want updateConfig", updates.Updates[0])
	}
	if len(perms.invalidated) != 1 || perms.invalidated[0] != userID {
		t.Fatalf("invalidated = %v, want [%d]", perms.invalidated, userID)
	}
}
