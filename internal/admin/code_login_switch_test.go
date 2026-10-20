package admin

import (
	"context"
	"testing"

	"telesrv/internal/identity"
)

// 验证码登录开关的服务端切换命令：与注册开关同构（三态 mode、试运行、幂等、审计），
// 另加防锁死——密码登录总开关关闭时拒绝把验证码登录切到关闭。

func newCodeLoginSwitchService(t *testing.T, codeLoginDefault, passwordLogin bool) (*Service, *memoryCommandRepo, *identity.Store) {
	t.Helper()
	store := identity.NewStore(t.TempDir())
	repo := newMemoryCommandRepo()
	svc := NewService(Dependencies{
		Commands:                repo,
		ServerIdentity:          store,
		CodeLoginEnabledDefault: func() bool { return codeLoginDefault },
		PasswordLoginEnabled:    func() bool { return passwordLogin },
		Now:                     fixedNow,
	})
	return svc, repo, store
}

func TestSetCodeLoginEnabledModesAndEffectiveState(t *testing.T) {
	ctx := context.Background()
	svc, repo, store := newCodeLoginSwitchService(t, false, true)
	for _, tc := range []struct {
		mode      string
		override  *bool
		effective bool
		source    string
	}{
		{"on", boolPtr(true), true, "override"},
		{"off", boolPtr(false), false, "override"},
		// follow_env 清除覆盖，跟随服务端自己的 TELESRV_CODE_LOGIN_ENABLED（这里为 false）。
		{"follow_env", nil, false, "env"},
	} {
		id := "code-login-" + tc.mode
		res, err := svc.SetCodeLoginEnabled(ctx, SetCodeLoginEnabledRequest{CommandMeta: serverIdentityMeta(id, false), Mode: " " + tc.mode + " "})
		if err != nil {
			t.Fatalf("mode %s: result=%+v err=%v", tc.mode, res, err)
		}
		info, err := store.Get()
		if err != nil {
			t.Fatal(err)
		}
		if (info.CodeLoginEnabled == nil) != (tc.override == nil) || (tc.override != nil && *info.CodeLoginEnabled != *tc.override) {
			t.Fatalf("mode %s: override = %v, want %v", tc.mode, info.CodeLoginEnabled, tc.override)
		}
		if info.SignUpEnabled != nil {
			t.Fatalf("mode %s: code login command touched the sign-up override", tc.mode)
		}
		details := storedDetails(t, repo, id)
		if details["mode"] != tc.mode || details["code_login_enabled_effective"] != tc.effective || details["code_login_source"] != tc.source {
			t.Fatalf("mode %s: audit details = %+v", tc.mode, details)
		}
	}
}

func TestSetCodeLoginEnabledIsIdempotentByCommandID(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newCodeLoginSwitchService(t, true, true)
	req := SetCodeLoginEnabledRequest{CommandMeta: serverIdentityMeta("code-login-once", false), Mode: "off"}
	if _, err := svc.SetCodeLoginEnabled(ctx, req); err != nil {
		t.Fatal(err)
	}
	// 绕过命令改掉文件：同一 command_id 重放只返回第一次的结果，不再写入。
	if err := store.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.SetCodeLoginEnabled(ctx, req)
	if err != nil || !replay.AlreadyExecuted {
		t.Fatalf("replay result=%+v err=%v, want already executed", replay, err)
	}
	if info, _ := store.Get(); info.CodeLoginEnabled != nil {
		t.Fatalf("replay wrote again: %v", *info.CodeLoginEnabled)
	}
	other := req
	other.Mode = "on"
	if conflict, err := svc.SetCodeLoginEnabled(ctx, other); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("reused id for another mode result=%+v err=%v, want COMMAND_ID_CONFLICT", conflict, err)
	}
}

// 防锁死：密码登录总开关关闭时，关闭验证码登录会让所有人都登录不进来。服务端拒绝这个
// 切换（试运行也拒绝，结论与正式执行一致），给出稳定错误码，不写入也不产生命令记录；
// 打开验证码登录、跟随环境变量（环境变量组合已由启动校验保证可登录）照常可用。
func TestSetCodeLoginEnabledRefusesToLockEveryoneOut(t *testing.T) {
	ctx := context.Background()
	svc, repo, store := newCodeLoginSwitchService(t, true, false)
	for _, dryRun := range []bool{true, false} {
		id := "lockout-dry"
		if !dryRun {
			id = "lockout-exec"
		}
		res, err := svc.SetCodeLoginEnabled(ctx, SetCodeLoginEnabledRequest{CommandMeta: serverIdentityMeta(id, dryRun), Mode: "off"})
		if err == nil || ErrorCode(err) != CodeCodeLoginLockout {
			t.Fatalf("dry=%v: result=%+v err=%v code=%q, want %s", dryRun, res, err, ErrorCode(err), CodeCodeLoginLockout)
		}
		if _, recorded := repo.items[id]; recorded {
			t.Fatalf("dry=%v: refused switch still ran as a command: %+v", dryRun, repo.items[id])
		}
	}
	if info, _ := store.Get(); info.CodeLoginEnabled != nil {
		t.Fatalf("refused switch wrote %v", *info.CodeLoginEnabled)
	}
	for _, mode := range []string{"on", "follow_env"} {
		if res, err := svc.SetCodeLoginEnabled(ctx, SetCodeLoginEnabledRequest{CommandMeta: serverIdentityMeta("lockout-"+mode, false), Mode: mode}); err != nil {
			t.Fatalf("mode %s with password login off: result=%+v err=%v", mode, res, err)
		}
	}
}

// 没接上环境默认值或密码登录总开关时不猜：直接拒绝，也不写入。
func TestSetCodeLoginEnabledRequiresItsDependencies(t *testing.T) {
	ctx := context.Background()
	for name, deps := range map[string]Dependencies{
		"no env default":     {PasswordLoginEnabled: func() bool { return true }},
		"no password switch": {CodeLoginEnabledDefault: func() bool { return true }},
	} {
		store := identity.NewStore(t.TempDir())
		deps.Commands = newMemoryCommandRepo()
		deps.ServerIdentity = store
		deps.Now = fixedNow
		svc := NewService(deps)
		if _, err := svc.SetCodeLoginEnabled(ctx, SetCodeLoginEnabledRequest{CommandMeta: serverIdentityMeta("missing", false), Mode: "on"}); err == nil || ErrorCode(err) != CodeServerIdentityUnavailable {
			t.Fatalf("%s: err=%v, want %s", name, err, CodeServerIdentityUnavailable)
		}
		if info, _ := store.Get(); info.CodeLoginEnabled != nil {
			t.Fatalf("%s: rejected change wrote %v", name, *info.CodeLoginEnabled)
		}
	}
}
