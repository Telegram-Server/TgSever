package admin

import (
	"context"
	"encoding/binary"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台推送开关命令（push.set_device_enabled）的命令层测试：设备存储用内存实现。

type pushDeviceFixture struct {
	svc     *Service
	repo    *memoryCommandRepo
	devices *memory.PushDeviceStore
}

const (
	pushOwnerID = int64(1001)
	pushOtherID = int64(1002)
)

// pushAuthKey 是账号详情里显示的 auth_key_id（authorizations 主键，小端 int64）对应的 key。
func pushAuthKey(id int64) [8]byte {
	var key [8]byte
	binary.LittleEndian.PutUint64(key[:], uint64(id))
	return key
}

func newPushDeviceFixture(t *testing.T) *pushDeviceFixture {
	t.Helper()
	ctx := context.Background()
	auths := memory.NewAuthorizationStore()
	devices := memory.NewPushDeviceStore(auths)
	for _, a := range []struct {
		id, user int64
	}{{-7001, pushOwnerID}, {7002, pushOwnerID}, {7003, pushOtherID}} {
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: pushAuthKey(a.id), UserID: a.user, Hash: a.id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []domain.PushDevice{
		{AuthKeyID: pushAuthKey(-7001), UserID: pushOwnerID, TokenType: domain.PushTokenAPNs, Token: "apns-1"},
		{AuthKeyID: pushAuthKey(-7001), UserID: pushOwnerID, TokenType: domain.PushTokenVoIP, Token: "voip-1"},
		{AuthKeyID: pushAuthKey(7002), UserID: pushOwnerID, TokenType: domain.PushTokenAPNs, Token: "apns-2"},
		{AuthKeyID: pushAuthKey(7003), UserID: pushOtherID, TokenType: domain.PushTokenAPNs, Token: "apns-3"},
	} {
		if err := devices.RegisterPushDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	repo := newMemoryCommandRepo()
	return &pushDeviceFixture{
		svc:     NewService(Dependencies{Commands: repo, PushDevices: devices, Now: fixedNow}),
		repo:    repo,
		devices: devices,
	}
}

func (f *pushDeviceFixture) enabled(t *testing.T, userID, authKeyID int64) []bool {
	t.Helper()
	list, err := f.devices.ListPushDevicesByUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	var out []bool
	for _, d := range list {
		if d.AuthKeyID == pushAuthKey(authKeyID) {
			out = append(out, d.PushEnabled)
		}
	}
	return out
}

func setPushRequest(commandID string, userID, authKeyID int64, enabled, dryRun bool) SetPushDeviceEnabledRequest {
	return SetPushDeviceEnabledRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "production build", DryRun: dryRun},
		UserID:      userID,
		AuthKeyID:   authKeyID,
		Enabled:     enabled,
	}
}

func TestSetPushDeviceEnabledSwitchesEveryRegistrationOfThatSession(t *testing.T) {
	f := newPushDeviceFixture(t)
	ctx := context.Background()

	res, err := f.svc.SetPushDeviceEnabled(ctx, setPushRequest("push-on", pushOwnerID, -7001, true, false))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionSetPushDeviceEnabled || res.TargetUserID != pushOwnerID ||
		res.Details["auth_key_id"] != "-7001" || res.Details["enabled"] != true || res.Details["updated_count"] != 2 {
		t.Fatalf("enable result = %+v", res)
	}
	if got := f.enabled(t, pushOwnerID, -7001); len(got) != 2 || !got[0] || !got[1] {
		t.Fatalf("session -7001 push = %v, want APNs and VoIP enabled", got)
	}
	if got := f.enabled(t, pushOwnerID, 7002); len(got) != 1 || got[0] {
		t.Fatalf("session 7002 push = %v, want untouched (disabled)", got)
	}
	rec := f.repo.items["push-on"]
	if rec.Action != ActionSetPushDeviceEnabled || rec.Actor != "ops" || rec.Reason != "production build" || rec.Status != domain.AdminCommandCompleted {
		t.Fatalf("audit record = %+v", rec)
	}

	if _, err := f.svc.SetPushDeviceEnabled(ctx, setPushRequest("push-off", pushOwnerID, -7001, false, false)); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := f.enabled(t, pushOwnerID, -7001); len(got) != 2 || got[0] || got[1] {
		t.Fatalf("session -7001 push after disable = %v, want both disabled", got)
	}
}

func TestSetPushDeviceEnabledDryRunChangesNothing(t *testing.T) {
	f := newPushDeviceFixture(t)
	res, err := f.svc.SetPushDeviceEnabled(context.Background(), setPushRequest("push-dry", pushOwnerID, -7001, true, true))
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !res.DryRun || res.Status != string(domain.AdminCommandCompleted) || res.Details["registration_count"] != 2 {
		t.Fatalf("dry-run result = %+v", res)
	}
	if got := f.enabled(t, pushOwnerID, -7001); len(got) != 2 || got[0] || got[1] {
		t.Fatalf("session push after dry-run = %v, want unchanged", got)
	}
}

// 别人的会话、没有推送登记的会话：PUSH_DEVICE_NOT_FOUND，什么都不改。
func TestSetPushDeviceEnabledRejectsSessionWithoutRegistration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		authKeyID int64
		dryRun    bool
	}{
		{"another account's session", 7003, false},
		{"another account's session, dry-run", 7003, true},
		{"unknown session", 9999, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPushDeviceFixture(t)
			_, err := f.svc.SetPushDeviceEnabled(context.Background(), setPushRequest("push-missing", pushOwnerID, tc.authKeyID, true, tc.dryRun))
			if ErrorCode(err) != CodePushDeviceNotFound {
				t.Fatalf("err = %v (code %q), want %s", err, ErrorCode(err), CodePushDeviceNotFound)
			}
			if got := f.enabled(t, pushOtherID, 7003); len(got) != 1 || got[0] {
				t.Fatalf("other account's push = %v, want untouched", got)
			}
		})
	}
}

func TestSetPushDeviceEnabledRequiresTarget(t *testing.T) {
	f := newPushDeviceFixture(t)
	for _, req := range []SetPushDeviceEnabledRequest{
		setPushRequest("no-user", 0, -7001, true, false),
		setPushRequest("no-key", pushOwnerID, 0, true, false),
	} {
		if _, err := f.svc.SetPushDeviceEnabled(context.Background(), req); err == nil {
			t.Fatalf("request %+v accepted, want rejected", req)
		}
	}
}
