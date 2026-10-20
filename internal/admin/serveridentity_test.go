package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// 服务器设置（identity 存储）的写入只由主服务器执行：后台经 admin API 把命令交过来，
// 由这里的 Service 写入服务端自己的 identity.Store。功能矩阵（每种写操作、试运行、
// 幂等、审计、参数校验）放在这一层测；admin API 与后台只证明转发接通。

func newServerIdentityService(t *testing.T, signUpDefault bool) (*Service, *memoryCommandRepo, *identity.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store := identity.NewStore(dir)
	repo := newMemoryCommandRepo()
	svc := NewService(Dependencies{
		Commands:             repo,
		ServerIdentity:       store,
		SignUpEnabledDefault: func() bool { return signUpDefault },
		Now:                  fixedNow,
	})
	return svc, repo, store, dir
}

func serverIdentityMeta(id string, dryRun bool) CommandMeta {
	return CommandMeta{CommandID: id, Actor: "ops", Reason: "rebrand", DryRun: dryRun}
}

// storedDetails 解出命令日志里保存的结果详情——审计行由 FinishCommand 从同一份
// 命令记录复制（见 postgres.AdminStore.FinishCommand），所以这里就是审计里的内容。
func storedDetails(t *testing.T, repo *memoryCommandRepo, id string) map[string]any {
	t.Helper()
	cmd, ok := repo.items[id]
	if !ok {
		t.Fatalf("command %s not recorded", id)
	}
	var result CommandResult
	if err := json.Unmarshal(cmd.ResultJSON, &result); err != nil {
		t.Fatalf("decode stored result of %s: %v (%s)", id, err, cmd.ResultJSON)
	}
	return result.Details
}

var pngIcon = []byte("\x89PNG\r\n\x1a\nicon bytes")

type serverIdentityCase struct {
	action string
	run    func(svc *Service, meta CommandMeta) (CommandResult, error)
	check  func(t *testing.T, store *identity.Store)
	// details 是审计里必须出现的详情键值。
	details map[string]any
}

func serverIdentityCases() []serverIdentityCase {
	ctx := context.Background()
	return []serverIdentityCase{
		{
			action: ActionSetServerIdentity,
			run: func(svc *Service, meta CommandMeta) (CommandResult, error) {
				return svc.SetServerIdentity(ctx, SetServerIdentityRequest{CommandMeta: meta, Name: " Acme ", Description: "Chat for Acme"})
			},
			check: func(t *testing.T, store *identity.Store) {
				if info, err := store.Get(); err != nil || info.Name != "Acme" || info.Description != "Chat for Acme" {
					t.Fatalf("identity = %+v err=%v, want name/description written", info, err)
				}
			},
			details: map[string]any{"name": " Acme ", "description": "Chat for Acme"},
		},
		{
			action: ActionSetWelcomeMessageTemplates,
			run: func(svc *Service, meta CommandMeta) (CommandResult, error) {
				return svc.SetWelcomeMessageTemplates(ctx, SetWelcomeMessageTemplatesRequest{CommandMeta: meta, PhoneTemplate: "hi {{server_name}}", EmailTemplate: ""})
			},
			check: func(t *testing.T, store *identity.Store) {
				if info, err := store.Get(); err != nil || info.WelcomeMessagePhoneTemplate != "hi {{server_name}}" || info.WelcomeMessageEmailTemplate != "" {
					t.Fatalf("identity = %+v err=%v, want phone welcome template written", info, err)
				}
			},
			details: map[string]any{"phone_template_set": true, "email_template_set": false},
		},
		{
			action: ActionSetLoginCodeMessageTemplate,
			run: func(svc *Service, meta CommandMeta) (CommandResult, error) {
				return svc.SetLoginCodeMessageTemplate(ctx, SetLoginCodeMessageTemplateRequest{CommandMeta: meta, Template: "code {{code}}"})
			},
			check: func(t *testing.T, store *identity.Store) {
				if info, err := store.Get(); err != nil || info.LoginCodeMessageTemplate != "code {{code}}" {
					t.Fatalf("identity = %+v err=%v, want login-code template written", info, err)
				}
			},
			details: map[string]any{"template_set": true},
		},
		{
			action: ActionSetSignUpEnabled,
			run: func(svc *Service, meta CommandMeta) (CommandResult, error) {
				return svc.SetSignUpEnabled(ctx, SetSignUpEnabledRequest{CommandMeta: meta, Mode: "off"})
			},
			check: func(t *testing.T, store *identity.Store) {
				if info, err := store.Get(); err != nil || info.SignUpEnabled == nil || *info.SignUpEnabled {
					t.Fatalf("identity = %+v err=%v, want sign-up override false", info, err)
				}
			},
			details: map[string]any{"mode": "off", "sign_up_enabled_effective": false, "sign_up_source": "override"},
		},
		{
			action: ActionUploadServerIcon,
			run: func(svc *Service, meta CommandMeta) (CommandResult, error) {
				return svc.UploadServerIcon(ctx, UploadServerIconRequest{CommandMeta: meta, FileName: "Logo.PNG", Data: pngIcon})
			},
			check: func(t *testing.T, store *identity.Store) {
				data, ext, ok := store.Icon()
				if !ok || ext != ".png" || !bytes.Equal(data, pngIcon) {
					t.Fatalf("icon ok=%v ext=%q bytes=%q, want the uploaded png", ok, ext, data)
				}
			},
			details: map[string]any{"bytes": float64(len(pngIcon)), "ext": ".png"},
		},
		{
			action: ActionRemoveServerIcon,
			run: func(svc *Service, meta CommandMeta) (CommandResult, error) {
				return svc.RemoveServerIcon(ctx, RemoveServerIconRequest{CommandMeta: meta})
			},
			check: func(t *testing.T, store *identity.Store) {
				if _, _, ok := store.Icon(); ok {
					t.Fatal("icon still present after removal")
				}
			},
		},
	}
}

func TestServerIdentityCommandsWriteToTheServerStoreAndAreAudited(t *testing.T) {
	svc, repo, store, _ := newServerIdentityService(t, true)
	for _, tc := range serverIdentityCases() {
		t.Run(tc.action, func(t *testing.T) {
			id := "exec-" + tc.action
			res, err := tc.run(svc, serverIdentityMeta(id, false))
			if err != nil || res.Status != string(domain.AdminCommandCompleted) || res.Action != tc.action || res.DryRun {
				t.Fatalf("result=%+v err=%v, want a completed %s", res, err, tc.action)
			}
			tc.check(t, store)

			cmd := repo.items[id]
			if cmd.Action != tc.action || cmd.Status != domain.AdminCommandCompleted || cmd.Actor != "ops" || cmd.Reason != "rebrand" || cmd.DryRun {
				t.Fatalf("recorded command = %+v, want completed %s by ops", cmd, tc.action)
			}
			details := storedDetails(t, repo, id)
			for key, want := range tc.details {
				if details[key] != want {
					t.Fatalf("audit details[%q] = %#v, want %#v (all: %+v)", key, details[key], want, details)
				}
			}
		})
	}
	// 上传图标的审计带内容摘要，不带图标字节本身。
	icon := repo.items["exec-"+ActionUploadServerIcon]
	if sha, _ := storedDetails(t, repo, "exec-"+ActionUploadServerIcon)["sha256"].(string); len(sha) != 64 {
		t.Fatalf("upload audit sha256 = %q, want a hex digest", sha)
	}
	if bytes.Contains(icon.RequestJSON, []byte("icon bytes")) || bytes.Contains(icon.ResultJSON, []byte("icon bytes")) {
		t.Fatalf("icon bytes leaked into the trail: %s %s", icon.RequestJSON, icon.ResultJSON)
	}
}

func TestServerIdentityCommandsDryRunWritesNothing(t *testing.T) {
	svc, repo, store, dir := newServerIdentityService(t, true)
	for _, tc := range serverIdentityCases() {
		id := "dry-" + tc.action
		res, err := tc.run(svc, serverIdentityMeta(id, true))
		if err != nil || !res.DryRun || res.Status != string(domain.AdminCommandCompleted) {
			t.Fatalf("%s dry run result=%+v err=%v", tc.action, res, err)
		}
		if cmd := repo.items[id]; cmd.Action != tc.action || !cmd.DryRun {
			t.Fatalf("%s dry run not recorded as a dry run: %+v", tc.action, cmd)
		}
	}
	if info, err := store.Get(); err != nil || info != (identity.Info{}) {
		t.Fatalf("dry runs changed identity: %+v err=%v", info, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("dry runs wrote files: %v err=%v", entries, err)
	}
}

func TestServerIdentityCommandsAreIdempotentByCommandID(t *testing.T) {
	ctx := context.Background()
	svc, _, store, _ := newServerIdentityService(t, true)
	req := SetServerIdentityRequest{CommandMeta: serverIdentityMeta("exec-name", false), Name: "Acme"}
	if _, err := svc.SetServerIdentity(ctx, req); err != nil {
		t.Fatal(err)
	}
	// 绕过命令改掉文件：同一 command_id 重放只返回第一次的结果，不再写入。
	if err := store.SetText("Changed elsewhere", ""); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.SetServerIdentity(ctx, req)
	if err != nil || !replay.AlreadyExecuted {
		t.Fatalf("replay result=%+v err=%v, want already executed", replay, err)
	}
	if info, _ := store.Get(); info.Name != "Changed elsewhere" {
		t.Fatalf("replay wrote again: %+v", info)
	}
	other := req
	other.Name = "Globex"
	if conflict, err := svc.SetServerIdentity(ctx, other); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("reused id for another name result=%+v err=%v, want COMMAND_ID_CONFLICT", conflict, err)
	}

	// 图标的内容摘要是请求的一部分：同一 command_id 换一张图是冲突，不是重放。
	upload := UploadServerIconRequest{CommandMeta: serverIdentityMeta("exec-icon", false), FileName: "a.png", Data: pngIcon}
	if _, err := svc.UploadServerIcon(ctx, upload); err != nil {
		t.Fatal(err)
	}
	if again, err := svc.UploadServerIcon(ctx, upload); err != nil || !again.AlreadyExecuted {
		t.Fatalf("icon replay result=%+v err=%v", again, err)
	}
	upload.Data = []byte("\x89PNG\r\n\x1a\nanother icon")
	if conflict, err := svc.UploadServerIcon(ctx, upload); err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("reused icon id with new bytes result=%+v err=%v, want COMMAND_ID_CONFLICT", conflict, err)
	}
	if data, _, _ := store.Icon(); !bytes.Equal(data, pngIcon) {
		t.Fatalf("conflicting upload replaced the icon: %q", data)
	}
}

func TestServerIdentityCommandsRejectInvalidInput(t *testing.T) {
	ctx := context.Background()
	svc, repo, store, _ := newServerIdentityService(t, true)
	tooBig := append(append([]byte{}, pngIcon...), bytes.Repeat([]byte{0}, MaxServerIconBytes)...)
	cases := []struct {
		name string
		run  func(meta CommandMeta) (CommandResult, error)
	}{
		{"login code without {{code}}", func(meta CommandMeta) (CommandResult, error) {
			return svc.SetLoginCodeMessageTemplate(ctx, SetLoginCodeMessageTemplateRequest{CommandMeta: meta, Template: "your code"})
		}},
		{"login code with {{code}} twice", func(meta CommandMeta) (CommandResult, error) {
			return svc.SetLoginCodeMessageTemplate(ctx, SetLoginCodeMessageTemplateRequest{CommandMeta: meta, Template: "{{code}} / {{code}}"})
		}},
		{"sign-up mode missing", func(meta CommandMeta) (CommandResult, error) {
			return svc.SetSignUpEnabled(ctx, SetSignUpEnabledRequest{CommandMeta: meta})
		}},
		{"sign-up mode unknown", func(meta CommandMeta) (CommandResult, error) {
			return svc.SetSignUpEnabled(ctx, SetSignUpEnabledRequest{CommandMeta: meta, Mode: "maybe"})
		}},
		{"icon extension", func(meta CommandMeta) (CommandResult, error) {
			return svc.UploadServerIcon(ctx, UploadServerIconRequest{CommandMeta: meta, FileName: "logo.bmp", Data: pngIcon})
		}},
		{"icon without extension", func(meta CommandMeta) (CommandResult, error) {
			return svc.UploadServerIcon(ctx, UploadServerIconRequest{CommandMeta: meta, FileName: "logo", Data: pngIcon})
		}},
		{"icon empty", func(meta CommandMeta) (CommandResult, error) {
			return svc.UploadServerIcon(ctx, UploadServerIconRequest{CommandMeta: meta, FileName: "logo.png"})
		}},
		{"icon too large", func(meta CommandMeta) (CommandResult, error) {
			return svc.UploadServerIcon(ctx, UploadServerIconRequest{CommandMeta: meta, FileName: "logo.png", Data: tooBig})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "bad-" + strings.ReplaceAll(tc.name, " ", "-")
			res, err := tc.run(serverIdentityMeta(id, false))
			if err == nil || res.Status == string(domain.AdminCommandCompleted) {
				t.Fatalf("result=%+v err=%v, want a rejection", res, err)
			}
			if ErrorCode(err) == "" {
				t.Fatalf("rejection %v carries no stable code", err)
			}
			if _, recorded := repo.items[id]; recorded {
				t.Fatalf("rejected input still ran as a command: %+v", repo.items[id])
			}
		})
	}
	if info, err := store.Get(); err != nil || info != (identity.Info{}) {
		t.Fatalf("rejected input changed identity: %+v err=%v", info, err)
	}

	// 清空登录码模板（空串）不需要 {{code}}：它回退到始终合法的默认文案。
	if res, err := svc.SetLoginCodeMessageTemplate(ctx, SetLoginCodeMessageTemplateRequest{CommandMeta: serverIdentityMeta("clear-login-code", false)}); err != nil {
		t.Fatalf("clearing the login-code template result=%+v err=%v", res, err)
	}
}

func TestSetSignUpEnabledModesAndEffectiveState(t *testing.T) {
	ctx := context.Background()
	svc, repo, store, _ := newServerIdentityService(t, false)
	for _, tc := range []struct {
		mode      string
		override  *bool
		effective bool
		source    string
	}{
		{"on", boolPtr(true), true, "override"},
		{"off", boolPtr(false), false, "override"},
		// follow_env 清除覆盖，跟随服务端自己的 TELESRV_SIGN_UP_ENABLED（这里为 false）。
		{"follow_env", nil, false, "env"},
	} {
		id := "signup-" + tc.mode
		res, err := svc.SetSignUpEnabled(ctx, SetSignUpEnabledRequest{CommandMeta: serverIdentityMeta(id, false), Mode: " " + tc.mode + " "})
		if err != nil {
			t.Fatalf("mode %s: result=%+v err=%v", tc.mode, res, err)
		}
		info, err := store.Get()
		if err != nil {
			t.Fatal(err)
		}
		if (info.SignUpEnabled == nil) != (tc.override == nil) || (tc.override != nil && *info.SignUpEnabled != *tc.override) {
			t.Fatalf("mode %s: override = %v, want %v", tc.mode, info.SignUpEnabled, tc.override)
		}
		details := storedDetails(t, repo, id)
		if details["mode"] != tc.mode || details["sign_up_enabled_effective"] != tc.effective || details["sign_up_source"] != tc.source {
			t.Fatalf("mode %s: audit details = %+v", tc.mode, details)
		}
	}
}

func TestServerIdentityCommandsRequireTheStore(t *testing.T) {
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow})
	_, err := svc.SetServerIdentity(context.Background(), SetServerIdentityRequest{CommandMeta: serverIdentityMeta("no-store", false), Name: "Acme"})
	if err == nil {
		t.Fatal("identity write without a store succeeded")
	}
	// 没接上 TELESRV_SIGN_UP_ENABLED 时不猜注册状态，直接拒绝，也不写入。
	store := identity.NewStore(t.TempDir())
	noDefault := NewService(Dependencies{Commands: newMemoryCommandRepo(), ServerIdentity: store, Now: fixedNow})
	if _, err := noDefault.SetSignUpEnabled(context.Background(), SetSignUpEnabledRequest{CommandMeta: serverIdentityMeta("no-default", false), Mode: "follow_env"}); err == nil {
		t.Fatal("sign-up change without the env default succeeded")
	}
	if info, _ := store.Get(); info.SignUpEnabled != nil {
		t.Fatalf("rejected sign-up change wrote %v", *info.SignUpEnabled)
	}
}

// 写入失败（例如目录只读）时命令记为失败，错误带回给调用方——不会报告成功。
func TestServerIdentityWriteFailureIsRecordedAsFailed(t *testing.T) {
	repo := newMemoryCommandRepo()
	svc := NewService(Dependencies{Commands: repo, ServerIdentity: failingIdentityStore{}, Now: fixedNow})
	res, err := svc.SetServerIdentity(context.Background(), SetServerIdentityRequest{CommandMeta: serverIdentityMeta("ro", false), Name: "Acme"})
	if !errors.Is(err, errReadOnlyIdentity) || res.Status != string(domain.AdminCommandFailed) {
		t.Fatalf("result=%+v err=%v, want the write error", res, err)
	}
	if repo.items["ro"].Status != domain.AdminCommandFailed {
		t.Fatalf("failed write recorded as %+v", repo.items["ro"])
	}
}

// 请求在文件写入之后被取消（例如后台的请求被掐断）时，命令仍然记完、审计照常落下：
// 文件写入不能回滚，所以命令记录不能因为取消而停在 running、没有审计。
func TestServerIdentityCommandFinishesAfterCancellation(t *testing.T) {
	repo := &ctxCheckingCommandRepo{memoryCommandRepo: newMemoryCommandRepo()}
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancellingIdentityStore{cancel: cancel}
	svc := NewService(Dependencies{Commands: repo, ServerIdentity: store, Now: fixedNow})
	res, err := svc.SetServerIdentity(ctx, SetServerIdentityRequest{CommandMeta: serverIdentityMeta("cancelled", false), Name: "Acme"})
	if err != nil || res.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("result=%+v err=%v, want the applied change reported as completed", res, err)
	}
	if !store.written || repo.items["cancelled"].Status != domain.AdminCommandCompleted {
		t.Fatalf("written=%v recorded=%+v, want the command finished", store.written, repo.items["cancelled"])
	}
}

// ctxCheckingCommandRepo 像真实存储一样在 context 已取消时拒绝写入。
type ctxCheckingCommandRepo struct{ *memoryCommandRepo }

func (r *ctxCheckingCommandRepo) BeginCommand(ctx context.Context, cmd domain.AdminCommand) (domain.AdminCommand, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.AdminCommand{}, false, err
	}
	return r.memoryCommandRepo.BeginCommand(ctx, cmd)
}

func (r *ctxCheckingCommandRepo) FinishCommand(ctx context.Context, id string, status domain.AdminCommandStatus, result []byte, errText string) (domain.AdminCommand, error) {
	if err := ctx.Err(); err != nil {
		return domain.AdminCommand{}, err
	}
	return r.memoryCommandRepo.FinishCommand(ctx, id, status, result, errText)
}

type cancellingIdentityStore struct {
	failingIdentityStore
	cancel  context.CancelFunc
	written bool
}

func (s *cancellingIdentityStore) SetText(string, string) error {
	s.written = true
	s.cancel()
	return nil
}

var errReadOnlyIdentity = errors.New("identity: mkdir: read-only file system")

type failingIdentityStore struct{}

func (failingIdentityStore) SetText(string, string) error { return errReadOnlyIdentity }
func (failingIdentityStore) SetWelcomeMessageTemplates(string, string) error {
	return errReadOnlyIdentity
}
func (failingIdentityStore) SetLoginCodeMessageTemplate(string) error { return errReadOnlyIdentity }
func (failingIdentityStore) SetSignUpOverride(*bool) error            { return errReadOnlyIdentity }
func (failingIdentityStore) SetIcon([]byte, string) error             { return errReadOnlyIdentity }
func (failingIdentityStore) RemoveIcon() error                        { return errReadOnlyIdentity }

func boolPtr(v bool) *bool { return &v }
