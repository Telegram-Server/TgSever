package admin

import (
	"context"
	"fmt"
	"testing"

	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 后台设置/清除设备上限命令（account.set_device_limit，#105）的命令层测试：内存的用户与授权
// store，命令通道（审计、试运行、按 command id 幂等与冲突检测）用 memoryCommandRepo。

type deviceLimitFixture struct {
	svc   *Service
	repo  *memoryCommandRepo
	users *memory.UserStore
	auths *memory.AuthorizationStore
}

func newDeviceLimitFixture(t *testing.T) *deviceLimitFixture {
	t.Helper()
	f := &deviceLimitFixture{repo: newMemoryCommandRepo(), users: memory.NewUserStore(), auths: memory.NewAuthorizationStore()}
	f.auths.LinkAuthorizationUsers(f.users)
	f.svc = NewService(Dependencies{
		Commands:     f.repo,
		Users:        users.NewService(f.users),
		DeviceLimits: f.auths,
		Now:          fixedNow,
	})
	return f
}

func (f *deviceLimitFixture) user(t *testing.T, u domain.User) domain.User {
	t.Helper()
	created, err := f.users.Create(context.Background(), u)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

func (f *deviceLimitFixture) assertLimit(t *testing.T, userID int64, want int, wantFound bool) {
	t.Helper()
	got, found, err := f.auths.GetDeviceLimit(context.Background(), userID)
	if err != nil || found != wantFound || got != want {
		t.Fatalf("device limit=%d found=%v err=%v, want %d found=%v", got, found, err, want, wantFound)
	}
}

func deviceLimitRequest(commandID string, userID int64, limit *int) SetDeviceLimitRequest {
	return SetDeviceLimitRequest{
		CommandMeta: CommandMeta{CommandID: commandID, Actor: "ops", Reason: "staff account"},
		UserID:      userID,
		DeviceLimit: limit,
	}
}

func intPtr(v int) *int { return &v }

func TestSetDeviceLimitSetsAndClears(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105101", FirstName: "Staff"})

	res, err := f.svc.SetDeviceLimit(ctx, deviceLimitRequest("exec-device-limit-1", u.ID, intPtr(2)))
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if res.Status != string(domain.AdminCommandCompleted) || res.Action != ActionSetDeviceLimit || res.TargetUserID != u.ID ||
		res.Details["previous_device_limit"] != nil || res.Details["device_limit"] != 2 || res.Details["would_change"] != true {
		t.Fatalf("set result = %+v", res)
	}
	f.assertLimit(t, u.ID, 2, true)

	res, err = f.svc.SetDeviceLimit(ctx, deviceLimitRequest("exec-device-limit-2", u.ID, intPtr(5)))
	if err != nil || res.Details["previous_device_limit"] != 2 || res.Details["device_limit"] != 5 {
		t.Fatalf("replace result = %+v, %v", res, err)
	}
	f.assertLimit(t, u.ID, 5, true)

	res, err = f.svc.SetDeviceLimit(ctx, deviceLimitRequest("exec-device-limit-3", u.ID, nil))
	if err != nil || res.Details["previous_device_limit"] != 5 || res.Details["device_limit"] != nil || res.Message != "device limit cleared" {
		t.Fatalf("clear result = %+v, %v", res, err)
	}
	f.assertLimit(t, u.ID, 0, false)

	if f.repo.items["exec-device-limit-3"].Action != ActionSetDeviceLimit || f.repo.items["exec-device-limit-3"].Actor != "ops" {
		t.Fatalf("command not recorded: %+v", f.repo.items["exec-device-limit-3"])
	}
}

func TestSetDeviceLimitDryRunWritesNothing(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105102", FirstName: "Dry"})

	req := deviceLimitRequest("dry-device-limit", u.ID, intPtr(3))
	req.DryRun = true
	res, err := f.svc.SetDeviceLimit(ctx, req)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun || res.Status != string(domain.AdminCommandCompleted) || res.Details["would_change"] != true {
		t.Fatalf("dry run result = %+v", res)
	}
	f.assertLimit(t, u.ID, 0, false)
	if f.repo.items["dry-device-limit"].Action != ActionSetDeviceLimit || !f.repo.items["dry-device-limit"].DryRun {
		t.Fatalf("dry run not recorded: %+v", f.repo.items["dry-device-limit"])
	}

	if err := f.auths.SetDeviceLimit(ctx, u.ID, 4); err != nil {
		t.Fatal(err)
	}
	clear := deviceLimitRequest("dry-device-limit-clear", u.ID, nil)
	clear.DryRun = true
	if res, err := f.svc.SetDeviceLimit(ctx, clear); err != nil || res.Details["previous_device_limit"] != 4 {
		t.Fatalf("dry clear = %+v, %v", res, err)
	}
	f.assertLimit(t, u.ID, 4, true)
}

func TestSetDeviceLimitIsIdempotentPerCommandIDAndConflictsOnAnotherValue(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105103", FirstName: "Replay"})
	req := deviceLimitRequest("exec-device-limit-replay", u.ID, intPtr(2))

	if _, err := f.svc.SetDeviceLimit(ctx, req); err != nil {
		t.Fatalf("set: %v", err)
	}
	// 之后被别处改过：同一个 command id 重放只返回原结果，不再写入。
	if err := f.auths.SetDeviceLimit(ctx, u.ID, 7); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.SetDeviceLimit(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("replay = %+v, %v; want an already-executed replay", again, err)
	}
	f.assertLimit(t, u.ID, 7, true)

	for _, other := range []*int{intPtr(3), nil} {
		conflict, err := f.svc.SetDeviceLimit(ctx, deviceLimitRequest("exec-device-limit-replay", u.ID, other))
		if err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
			t.Fatalf("same command id with device_limit=%v = %+v, %v; want COMMAND_ID_CONFLICT", other, conflict, err)
		}
	}
	f.assertLimit(t, u.ID, 7, true)
}

func TestSetDeviceLimitRejectsSystemBotDeletedAndMissingAccounts(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	bot := f.user(t, domain.User{FirstName: "Bot", Bot: true, Username: "limit_bot"})
	deleted := f.user(t, domain.User{Phone: "15550105104", FirstName: "Gone", Deleted: true})

	for _, tc := range []struct {
		name   string
		userID int64
	}{
		{"system", domain.OfficialSystemUserID},
		{"bot", bot.ID},
		{"deleted", deleted.ID},
		{"missing", 987654321},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dry := range []bool{true, false} {
				for _, limit := range []*int{intPtr(1), nil} {
					req := deviceLimitRequest(fmt.Sprintf("reject-%s-%v-%v", tc.name, dry, limit == nil), tc.userID, limit)
					req.DryRun = dry
					res, err := f.svc.SetDeviceLimit(ctx, req)
					if err == nil || res.Code != "USER_INVALID" {
						t.Fatalf("dry=%v clear=%v result = %+v, %v; want USER_INVALID", dry, limit == nil, res, err)
					}
					if f.repo.items[req.CommandID].Status != domain.AdminCommandFailed {
						t.Fatalf("rejection not recorded as failed: %+v", f.repo.items[req.CommandID])
					}
				}
			}
			f.assertLimit(t, tc.userID, 0, false)
		})
	}
}

func TestSetDeviceLimitRejectsValuesOutsideTheRange(t *testing.T) {
	f := newDeviceLimitFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105105", FirstName: "Range"})

	for _, limit := range []int{0, -1, domain.MaxDeviceLimit + 1} {
		req := deviceLimitRequest(fmt.Sprintf("invalid-device-limit-%d", limit), u.ID, intPtr(limit))
		res, err := f.svc.SetDeviceLimit(ctx, req)
		if err == nil || ErrorCode(err) != "DEVICE_LIMIT_INVALID" {
			t.Fatalf("device_limit=%d result = %+v, %v; want DEVICE_LIMIT_INVALID", limit, res, err)
		}
		if _, recorded := f.repo.items[req.CommandID]; recorded {
			t.Fatalf("device_limit=%d: an invalid request was recorded as a command", limit)
		}
	}
	f.assertLimit(t, u.ID, 0, false)
	for _, limit := range []int{domain.MinDeviceLimit, domain.MaxDeviceLimit} {
		if _, err := f.svc.SetDeviceLimit(ctx, deviceLimitRequest(fmt.Sprintf("edge-device-limit-%d", limit), u.ID, intPtr(limit))); err != nil {
			t.Fatalf("device_limit=%d: %v", limit, err)
		}
		f.assertLimit(t, u.ID, limit, true)
	}
}
