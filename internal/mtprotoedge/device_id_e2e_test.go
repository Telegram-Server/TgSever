package mtprotoedge

import (
	"context"
	"testing"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"
	"go.uber.org/zap/zaptest"
)

// TestInitConnectionDeviceIDEndToEnd 只验证接线（#111）：客户端在 initConnection.params 里
// 上报的 device_id 经真实的 MTProto 连接落到它的 auth key 上；同一个 auth key 重连时
// initConnection 不再带 device_id，已持久化的值保持不变。params 解析与非法值矩阵在
// internal/rpc，非空才覆盖的语义在 storetest。
func TestInitConnectionDeviceIDEndToEnd(t *testing.T) {
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{})
	storage := &session.StorageMemory{}

	withDeviceID := telegram.DeviceConfig{
		DeviceModel: "Desktop", SystemVersion: "Windows 11", AppVersion: "6.0.0 x64",
		SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
		Params: &tg.JSONObject{Value: []tg.JSONObjectValue{
			{Key: "tz_offset", Value: &tg.JSONNumber{Value: 0}},
			{Key: "device_id", Value: &tg.JSONString{Value: "desk-e2e-0001"}},
		}},
	}
	srv.runWithDevice(t, storage, withDeviceID)

	authKeyID := loadSessionAuthKeyID(t, storage)
	assertServerAuthKeyDeviceID(t, srv, authKeyID, "desk-e2e-0001")

	withoutDeviceID := withDeviceID
	withoutDeviceID.Params = &tg.JSONObject{Value: []tg.JSONObjectValue{{Key: "tz_offset", Value: &tg.JSONNumber{Value: 0}}}}
	srv.runWithDevice(t, storage, withoutDeviceID)
	if again := loadSessionAuthKeyID(t, storage); again != authKeyID {
		t.Fatalf("reconnect used auth key %x, want the same key %x", again, authKeyID)
	}
	assertServerAuthKeyDeviceID(t, srv, authKeyID, "desk-e2e-0001")
}

// runWithDevice 用给定的会话存储（决定 auth key 是否复用）和 initConnection 设备信息连一次
// 服务端，发一个 help.getConfig 后断开。
func (s *loginPasswordServer) runWithDevice(t *testing.T, storage session.Storage, device telegram.DeviceConfig) {
	t.Helper()
	client := telegram.NewClient(1, "hash", telegram.Options{
		PublicKeys:     []exchange.PublicKey{{RSA: &s.rsaKey.PublicKey}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		DCList:         dcs.List{Options: []tg.DCOption{{ID: loginPasswordTestDC, IPAddress: s.addr.IP.String(), Port: s.addr.Port, Static: true}}},
		Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
		SessionStorage: storage,
		Device:         device,
		// 不设 UpdateHandler 时 gotd 会在最外层套 invokeWithoutUpdates，服务端不接受。
		UpdateHandler: telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
	})
	if err := client.Run(s.ctx, func(ctx context.Context) error {
		_, err := tg.NewClient(client).HelpGetConfig(ctx)
		return err
	}); err != nil {
		t.Fatalf("client run: %v", err)
	}
}

func loadSessionAuthKeyID(t *testing.T, storage session.Storage) [8]byte {
	t.Helper()
	data, err := (&session.Loader{Storage: storage}).Load(context.Background())
	if err != nil {
		t.Fatalf("load client session: %v", err)
	}
	var id [8]byte
	if len(data.AuthKeyID) != len(id) {
		t.Fatalf("client session auth key id length = %d", len(data.AuthKeyID))
	}
	copy(id[:], data.AuthKeyID)
	return id
}

func assertServerAuthKeyDeviceID(t *testing.T, srv *loginPasswordServer, authKeyID [8]byte, want string) {
	t.Helper()
	key, found, err := srv.authKeys.Revalidate(context.Background(), authKeyID)
	if err != nil || !found {
		t.Fatalf("server auth key %x found=%v err=%v", authKeyID, found, err)
	}
	if key.DeviceID != want {
		t.Fatalf("server auth key device_id = %q, want %q", key.DeviceID, want)
	}
}
