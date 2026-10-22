package admin

import (
	"context"
	"fmt"
	"testing"

	"telesrv/internal/domain"
)

// 后台解绑设备命令（account.unbind_device，#114）的命令层测试，fixture 与设置设备上限共用。

func unbindDeviceRequest(commandID string, userID int64, deviceID string) UnbindDeviceRequest {
	return UnbindDeviceRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "replaced laptop"},
		UserID:      userID,
		DeviceID:    deviceID,
	}
}

func (f *deviceLimitFixture) assertSignedIn(t *testing.T, key [8]byte, want bool) {
	t.Helper()
	_, found, err := f.auths.ByAuthKey(context.Background(), key)
	if err != nil || found != want {
		t.Fatalf("authorization on %x found=%v err=%v, want %v", key, found, err, want)
	}
}

func TestUnbindDeviceFreesTheSlotAndSignsTheDeviceOut(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105201", FirstName: "Staff"})
	if _, err := f.auths.SetDeviceLimit(ctx, u.ID, 2); err != nil {
		t.Fatal(err)
	}
	laptop1, laptop2 := f.signIn(t, u.ID, "laptop"), f.signIn(t, u.ID, "laptop")
	phone := f.signIn(t, u.ID, "phone")
	f.assertBound(t, u.ID, "laptop", "phone")

	res, err := f.svc.UnbindDevice(ctx, unbindDeviceRequest("exec-unbind-1", u.ID, "laptop"))
	if err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionUnbindDevice || res.TargetUserID != u.ID ||
		res.Details["device_id"] != "laptop" || res.Details["revoked_count"] != 2 {
		t.Fatalf("unbind result = %+v", res)
	}
	f.assertBound(t, u.ID, "phone")
	f.assertSignedIn(t, laptop1, false)
	f.assertSignedIn(t, laptop2, false)
	f.assertSignedIn(t, phone, true)
	if got := fmt.Sprint(f.revoker.keys); got != fmt.Sprint([][8]byte{laptop1, laptop2}) {
		t.Fatalf("closed connections on %v, want both laptop keys", got)
	}
	// 名额已释放：新设备能登录。
	f.signIn(t, u.ID, "new-laptop")
	f.assertBound(t, u.ID, "phone", "new-laptop")

	rec := f.repo.items["exec-unbind-1"]
	if rec.Action != ActionUnbindDevice || rec.Actor != "ops" || rec.Reason != "replaced laptop" || rec.Status != domain.AdminCommandCompleted {
		t.Fatalf("command not audited: %+v", rec)
	}
}

func TestUnbindDeviceDryRunWritesNothing(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105202", FirstName: "Dry"})
	if _, err := f.auths.SetDeviceLimit(ctx, u.ID, 1); err != nil {
		t.Fatal(err)
	}
	key := f.signIn(t, u.ID, "laptop")

	req := unbindDeviceRequest("dry-unbind", u.ID, "laptop")
	req.DryRun = true
	res, err := f.svc.UnbindDevice(ctx, req)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun || res.Status != string(domain.AdminCommandCompleted) || res.Details["revoked_count"] != 1 {
		t.Fatalf("dry run result = %+v", res)
	}
	f.assertBound(t, u.ID, "laptop")
	f.assertSignedIn(t, key, true)
	if len(f.revoker.keys) != 0 {
		t.Fatalf("dry run closed connections on %v", f.revoker.keys)
	}
	if rec := f.repo.items["dry-unbind"]; rec.Action != ActionUnbindDevice || !rec.DryRun {
		t.Fatalf("dry run not recorded: %+v", rec)
	}
}

func TestUnbindDeviceIsIdempotentPerCommandIDAndConflictsOnAnotherDevice(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105203", FirstName: "Replay"})
	if _, err := f.auths.SetDeviceLimit(ctx, u.ID, 2); err != nil {
		t.Fatal(err)
	}
	f.signIn(t, u.ID, "laptop")
	f.signIn(t, u.ID, "phone")
	req := unbindDeviceRequest("exec-unbind-replay", u.ID, "laptop")

	if _, err := f.svc.UnbindDevice(ctx, req); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	// 同一台设备之后又绑定回来：同一个 command id 重放只返回原结果，不会再次解绑。
	again := f.signIn(t, u.ID, "laptop")
	res, err := f.svc.UnbindDevice(ctx, req)
	// 重放的结果是从命令记录里解码出来的，数字是 JSON 数字。
	if err != nil || !res.AlreadyExecuted || fmt.Sprint(res.Details["revoked_count"]) != "1" {
		t.Fatalf("replay = %+v, %v; want an already-executed replay", res, err)
	}
	f.assertBound(t, u.ID, "phone", "laptop")
	f.assertSignedIn(t, again, true)

	conflict, err := f.svc.UnbindDevice(ctx, unbindDeviceRequest("exec-unbind-replay", u.ID, "phone"))
	if err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("same command id for another device = %+v, %v; want COMMAND_ID_CONFLICT", conflict, err)
	}
	f.assertBound(t, u.ID, "phone", "laptop")
}

func TestUnbindDeviceRejectsInvalidTargets(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	bot := f.user(t, domain.User{FirstName: "Bot", Bot: true, Username: "unbind_bot"})
	deleted := f.user(t, domain.User{Phone: "15550105204", FirstName: "Gone", Deleted: true})
	staff := f.user(t, domain.User{Phone: "15550105205", FirstName: "Staff"})
	if _, err := f.auths.SetDeviceLimit(ctx, staff.ID, 2); err != nil {
		t.Fatal(err)
	}
	key := f.signIn(t, staff.ID, "laptop")

	for _, tc := range []struct {
		name     string
		userID   int64
		deviceID string
		code     string
	}{
		{"system", domain.OfficialSystemUserID, "laptop", "USER_INVALID"},
		{"bot", bot.ID, "laptop", "USER_INVALID"},
		{"deleted", deleted.ID, "laptop", "USER_INVALID"},
		{"missing", 987654321, "laptop", "USER_INVALID"},
		{"not bound", staff.ID, "desktop", "DEVICE_BINDING_NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dry := range []bool{true, false} {
				req := unbindDeviceRequest(fmt.Sprintf("reject-unbind-%s-%v", tc.name, dry), tc.userID, tc.deviceID)
				req.DryRun = dry
				res, err := f.svc.UnbindDevice(ctx, req)
				if err == nil || res.Code != tc.code {
					t.Fatalf("dry=%v result = %+v, %v; want %s", dry, res, err, tc.code)
				}
				if f.repo.items[req.CommandID].Status != domain.AdminCommandFailed {
					t.Fatalf("rejection not recorded as failed: %+v", f.repo.items[req.CommandID])
				}
			}
		})
	}
	f.assertBound(t, staff.ID, "laptop")
	f.assertSignedIn(t, key, true)
	if len(f.revoker.keys) != 0 {
		t.Fatalf("rejected commands closed connections on %v", f.revoker.keys)
	}
}

func TestUnbindDeviceRequiresUserAndDeviceID(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	for _, req := range []UnbindDeviceRequest{
		unbindDeviceRequest("unbind-no-user", 0, "laptop"),
		unbindDeviceRequest("unbind-no-device", 42, ""),
	} {
		if _, err := f.svc.UnbindDevice(ctx, req); err == nil {
			t.Fatalf("request %+v accepted", req)
		}
		if _, recorded := f.repo.items[req.CommandID]; recorded {
			t.Fatalf("invalid request %+v was recorded as a command", req)
		}
	}
}
