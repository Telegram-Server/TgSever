package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// initConnection.params 顶层对象里的字符串键 device_id 被解析并持久化到 auth key 上
// （#111）。缺失、非字符串、不合法一律视为未上报，不写入。
func deviceIDParamsCases() []struct {
	name   string
	params tg.JSONValueClass
	want   string
} {
	obj := func(values ...tg.JSONObjectValue) tg.JSONValueClass { return &tg.JSONObject{Value: values} }
	str := func(key, value string) tg.JSONObjectValue {
		return tg.JSONObjectValue{Key: key, Value: &tg.JSONString{Value: value}}
	}
	return []struct {
		name   string
		params tg.JSONValueClass
		want   string
	}{
		{name: "valid", params: obj(str("tz_offset", "0"), str("device_id", "desk-0001")), want: "desk-0001"},
		{name: "max length", params: obj(str("device_id", strings.Repeat("d", 128))), want: strings.Repeat("d", 128)},
		{name: "no params"},
		{name: "params without device_id", params: obj(str("tz_offset", "0"))},
		{name: "empty", params: obj(str("device_id", ""))},
		{name: "too long", params: obj(str("device_id", strings.Repeat("d", 129)))},
		{name: "non printable", params: obj(str("device_id", "desk\x01"))},
		{name: "non ascii", params: obj(str("device_id", "设备"))},
		{name: "number", params: obj(tg.JSONObjectValue{Key: "device_id", Value: &tg.JSONNumber{Value: 42}})},
		{name: "nested object", params: obj(tg.JSONObjectValue{Key: "device", Value: obj(str("device_id", "desk-0001"))})},
		{name: "top level not an object", params: &tg.JSONString{Value: "desk-0001"}},
	}
}

func newDeviceIDTestRouter(t *testing.T) (*Router, *memory.AuthKeyStore, [8]byte) {
	t.Helper()
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	if err := keys.Save(context.Background(), store.AuthKeyData{ID: authKeyID, ServerSalt: 1}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: appauth.NewService(nil, nil, nil, keys, nil, "12345"),
	}, zaptest.NewLogger(t), clock.System)
	return r, keys, authKeyID
}

func deviceIDInitRequest(layer int, params tg.JSONValueClass) *tg.InvokeWithLayerRequest {
	init := &tg.InitConnectionRequest{
		APIID:          2040,
		DeviceModel:    "Desktop",
		SystemVersion:  "Windows 11",
		AppVersion:     "6.0.0 x64",
		SystemLangCode: "en",
		LangPack:       "tdesktop",
		LangCode:       "en",
		Query:          &tg.HelpGetConfigRequest{},
	}
	if params != nil {
		init.SetParams(params)
	}
	return &tg.InvokeWithLayerRequest{Layer: layer, Query: init}
}

func assertPersistedDeviceID(t *testing.T, keys *memory.AuthKeyStore, authKeyID [8]byte, want string) {
	t.Helper()
	data, found, err := keys.Revalidate(context.Background(), authKeyID)
	if err != nil || !found {
		t.Fatalf("revalidate auth key found=%v err=%v", found, err)
	}
	if data.DeviceID != want {
		t.Fatalf("persisted device_id = %q, want %q", data.DeviceID, want)
	}
	if data.DeviceModel != "Desktop" {
		t.Fatalf("persisted device_model = %q, want Desktop (initConnection metadata must still persist)", data.DeviceModel)
	}
}

func TestLayerAdmissionPersistsInitConnectionDeviceID(t *testing.T) {
	for _, tc := range deviceIDParamsCases() {
		t.Run(tc.name, func(t *testing.T) {
			r, keys, authKeyID := newDeviceIDTestRouter(t)
			var body bin.Buffer
			if err := deviceIDInitRequest(int(tlprofile.Profile225), tc.params).Encode(&body); err != nil {
				t.Fatal(err)
			}
			admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if _, _, err := r.DispatchAdmitted(context.Background(), authKeyID, 10, 0, 0, admitted); err != nil {
				t.Fatalf("dispatch admitted: %v", err)
			}
			assertPersistedDeviceID(t, keys, authKeyID, tc.want)
		})
	}
}

func TestLegacyDispatchPersistsInitConnectionDeviceID(t *testing.T) {
	for _, tc := range deviceIDParamsCases() {
		t.Run(tc.name, func(t *testing.T) {
			r, keys, authKeyID := newDeviceIDTestRouter(t)
			var body bin.Buffer
			if err := deviceIDInitRequest(currentClientLayer, tc.params).Encode(&body); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Dispatch(context.Background(), authKeyID, 10, &body); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			assertPersistedDeviceID(t, keys, authKeyID, tc.want)
		})
	}
}

// edge 把重发的 initConnection 别名到已在执行的请求时，只通过 ObserveInitConnection
// 记录元数据；device_id 同样要落库。
func TestObserveInitConnectionPersistsDeviceID(t *testing.T) {
	r, keys, authKeyID := newDeviceIDTestRouter(t)
	params := &tg.JSONObject{Value: []tg.JSONObjectValue{{Key: "device_id", Value: &tg.JSONString{Value: "desk-0002"}}}}
	if err := r.ObserveInitConnection(context.Background(), authKeyID, 10, int(tlprofile.Profile225), 2040,
		"Desktop", "Windows 11", "6.0.0 x64", "en", "tdesktop", "en", params); err != nil {
		t.Fatalf("observe init connection: %v", err)
	}
	assertPersistedDeviceID(t, keys, authKeyID, "desk-0002")
}
