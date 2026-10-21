package rpc

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// 接线：account.registerDevice 把登记（含 initConnection 的 lang_code）存进 PushDevices，
// account.unregisterDevice 删掉对应类型，auth.logOut 之后登记随授权一起消失。
func TestAccountRegisterDeviceIsStoredUntilLogOut(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{0x94, 0x01}
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	devices := memory.NewPushDeviceStore(authorizations)
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "15550009494", FirstName: "Push"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authKeys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: user.ID, Hash: 9494}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Auth:        appauth.NewService(users, authorizations, nil, authKeys, nil, "12345"),
		PushDevices: devices,
	}, zaptest.NewLogger(t), clock.System)

	secret := bytes.Repeat([]byte{0x33}, 256)
	dispatch := func(req bin.Encoder) any {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		result, err := r.Dispatch(ctx, authKeyID, 1, &buf)
		if err != nil {
			t.Fatalf("dispatch %T: %v", req, err)
		}
		return dispatchCanonicalValue(result)
	}
	register := &tg.AccountRegisterDeviceRequest{
		NoMuted: true, TokenType: domain.PushTokenAPNs, Token: "a1b2c3", AppSandbox: true,
		Secret: secret, OtherUIDs: []int64{77, 78},
	}
	if got := dispatch(&tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: &tg.InitConnectionRequest{
		APIID: 1, DeviceModel: "iPhone", SystemVersion: "18", AppVersion: "11", SystemLangCode: "en-US",
		LangPack: "ios", LangCode: "ru", Query: register,
	}}); got != true {
		t.Fatalf("registerDevice = %#v, want true", got)
	}
	if got := dispatch(&tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenVoIP, Token: "voip1", Secret: secret}); got != true {
		t.Fatalf("registerDevice voip = %#v, want true", got)
	}
	if got := dispatch(&tg.AccountUnregisterDeviceRequest{TokenType: domain.PushTokenVoIP, Token: "voip1"}); got != true {
		t.Fatalf("unregisterDevice = %#v, want true", got)
	}

	list, err := devices.ListPushDevicesByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("devices = %+v, want only the APNs registration", list)
	}
	got := list[0]
	if got.AuthKeyID != authKeyID || got.UserID != user.ID || got.TokenType != domain.PushTokenAPNs || got.Token != "a1b2c3" ||
		!got.AppSandbox || !got.NoMuted || !bytes.Equal(got.Secret, secret) || !slices.Equal(got.OtherUIDs, []int64{77, 78}) ||
		got.LangCode != "ru" {
		t.Fatalf("device = %+v, want the registered fields with lang ru", got)
	}

	if _, ok := dispatch(&tg.AuthLogOutRequest{}).(*tg.AuthLoggedOut); !ok {
		t.Fatal("auth.logOut did not return auth.loggedOut")
	}
	if list, err := devices.ListPushDevicesByUser(ctx, user.ID); err != nil || len(list) != 0 {
		t.Fatalf("devices after logout = %+v err=%v, want none", list, err)
	}
}

// token 轮换：iOS 先 registerDevice(新 token)，再 unregisterDevice(旧 token)；新登记必须保留。
func TestAccountUnregisterDeviceWithOldTokenKeepsRotatedRegistration(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{0x94, 0x02}
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	devices := memory.NewPushDeviceStore(authorizations)
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "15550009495", FirstName: "Push"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authKeys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: user.ID, Hash: 9495}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Auth:        appauth.NewService(users, authorizations, nil, authKeys, nil, "12345"),
		PushDevices: devices,
	}, zaptest.NewLogger(t), clock.System)
	dispatch := func(req bin.Encoder) any {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		result, err := r.Dispatch(ctx, authKeyID, 1, &buf)
		if err != nil {
			t.Fatalf("dispatch %T: %v", req, err)
		}
		return dispatchCanonicalValue(result)
	}
	for _, token := range []string{"old-token", "new-token"} {
		if got := dispatch(&tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenAPNs, Token: token}); got != true {
			t.Fatalf("registerDevice %s = %#v, want true", token, got)
		}
	}
	if got := dispatch(&tg.AccountUnregisterDeviceRequest{TokenType: domain.PushTokenAPNs, Token: "old-token"}); got != true {
		t.Fatalf("unregisterDevice = %#v, want true", got)
	}
	list, err := devices.ListPushDevicesByUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Token != "new-token" {
		t.Fatalf("devices after unregistering the old token = %+v, want new-token kept", list)
	}
}
