package loginpassword_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store/memory"
)

// 管理登录密码的业务层单测只覆盖端到端测试构造不出来的情形：管理用 challenge 过期（注入
// 时钟）、重设窗口的边界（注入时钟）、bot 账号、通知发送失败、删除一个本来就不存在的密码。其余行为以
// internal/mtprotoedge/login_password_manage_e2e_test.go 为准。

// recordingNotices 记录发出的 777000 通知；err 非空时每次发送都失败。
type recordingNotices struct {
	mu   sync.Mutex
	sent []domain.SendPrivateTextRequest
	err  error
}

func (r *recordingNotices) SendPrivateText(_ context.Context, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return domain.SendPrivateTextResult{}, r.err
	}
	r.sent = append(r.sent, req)
	return domain.SendPrivateTextResult{}, nil
}

func (r *recordingNotices) messages() []domain.SendPrivateTextRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.SendPrivateTextRequest(nil), r.sent...)
}

func newManageFixture(t *testing.T, notices *recordingNotices, log *zap.Logger, opts ...loginpassword.Option) *fixture {
	t.Helper()
	f := &fixture{users: memory.NewUserStore(), flows: &fakeFlows{}, now: time.Unix(1_800_000_000, 0)}
	f.passwords = memory.NewLoginPasswordStore(f.users)
	opts = append([]loginpassword.Option{loginpassword.WithEnabled(true), loginpassword.WithClock(f.clock), loginpassword.WithChangeNotices(notices, log)}, opts...)
	f.svc = loginpassword.NewService(f.users, f.passwords, memory.NewLoginPasswordChallengeStore(f.clock), f.flows, opts...)
	return f
}

// newSettings 按客户端设置新密码的方式构造 new_settings。
func newSettings(t *testing.T, password string) domain.PasswordInputSettings {
	t.Helper()
	pw := newPassword(t, password)
	algo := srp.DefaultAlgo()
	return domain.PasswordInputSettings{
		NewAlgo:         &domain.PasswordKDFAlgo{Salt1: pw.Salt1, Salt2: pw.Salt2, G: algo.G, P: algo.P},
		NewPasswordHash: pw.Verifier,
	}
}

func TestUpdateMyLoginPasswordRejectsAnExpiredChallenge(t *testing.T) {
	notices := &recordingNotices{}
	f := newManageFixture(t, notices, zap.NewNop())
	ctx := context.Background()
	u := f.account(t, domain.User{Phone: "15550341001", FirstName: "Late"}, "pw")

	settings, err := f.svc.GetMyLoginPassword(ctx, testAuthKey, u.ID)
	if err != nil {
		t.Fatalf("GetMyLoginPassword: %v", err)
	}
	check := proof(t, settings, "pw")
	f.advance(5 * time.Minute)
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, nil, check, newSettings(t, "next")); !errors.Is(err, domain.ErrSRPIDInvalid) {
		t.Fatalf("UpdateMyLoginPassword after the TTL = %v, want ErrSRPIDInvalid", err)
	}
	if len(notices.messages()) != 0 {
		t.Fatal("a rejected update sent a notice")
	}
}

func TestUpdateMyLoginPasswordResetWindowEndsAtTheWindow(t *testing.T) {
	notices := &recordingNotices{}
	f := newManageFixture(t, notices, zap.NewNop(), loginpassword.WithResetWindow(10*time.Minute))
	ctx := context.Background()
	u := f.account(t, domain.User{Phone: "15550341010", FirstName: "Forgot"}, "forgotten")
	signedInAt := f.clock()
	empty := domain.PasswordCheck{Empty: true}

	// 授权创建满 10 分钟：窗口已过，空 current 被拒绝。
	f.advance(10 * time.Minute)
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, signedInAtLookup(signedInAt), empty, newSettings(t, "late")); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("empty current 10m after login = %v, want ErrPasswordHashInvalid", err)
	}
	// 没有读到授权（零值）同样不放行。
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, signedInAtLookup(time.Time{}), empty, newSettings(t, "unknown")); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("empty current without a login time = %v, want ErrPasswordHashInvalid", err)
	}
	if len(notices.messages()) != 0 {
		t.Fatal("a rejected reset sent a notice")
	}

	// 刚过一秒的授权还在窗口内：重设成功，777000 发的是区别于普通修改的"免旧密码重设"通知。
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, signedInAtLookup(f.clock().Add(-time.Second)), empty, newSettings(t, "reset")); err != nil {
		t.Fatalf("empty current 1s after login = %v, want nil", err)
	}
	got := notices.messages()
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "Login password reset.\n") ||
		!strings.Contains(got[0].Message, "without the old password") {
		t.Fatalf("notices after the reset = %+v, want one \"Login password reset.\" notice", got)
	}
}

// 授权的创建时间晚于服务端当前时间（时钟回拨、多实例时钟不齐）不算在窗口内：
// 窗口只认 0 <= now-createdAt < window。
func TestUpdateMyLoginPasswordResetWindowIgnoresAFutureLogin(t *testing.T) {
	notices := &recordingNotices{}
	f := newManageFixture(t, notices, zap.NewNop(), loginpassword.WithResetWindow(10*time.Minute))
	ctx := context.Background()
	u := f.account(t, domain.User{Phone: "15550341011", FirstName: "Future"}, "kept")
	empty := domain.PasswordCheck{Empty: true}

	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, signedInAtLookup(f.clock().Add(time.Minute)), empty, newSettings(t, "future")); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("empty current with a login 1m in the future = %v, want ErrPasswordHashInvalid", err)
	}
	if len(notices.messages()) != 0 {
		t.Fatal("a rejected reset sent a notice")
	}
	// 恰好此刻创建的授权（age == 0）在窗口内。
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, signedInAtLookup(f.clock()), empty, newSettings(t, "now")); err != nil {
		t.Fatalf("empty current with a login right now = %v, want nil", err)
	}
}

// 授权只在"已有登录密码且 current 为空、窗口打开"时才读：首次设置、带 current 的修改、
// 窗口关闭时都不碰授权，即使读取会失败。
func TestUpdateMyLoginPasswordLooksUpTheLoginOnlyForAReset(t *testing.T) {
	ctx := context.Background()
	lookupErr := errors.New("authorization store down")
	calls := 0
	failing := func(context.Context) (time.Time, error) {
		calls++
		return time.Time{}, lookupErr
	}

	f := newManageFixture(t, &recordingNotices{}, zap.NewNop(), loginpassword.WithResetWindow(10*time.Minute))
	u, err := f.users.Create(ctx, domain.User{Phone: "15550341012", FirstName: "First"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, failing, domain.PasswordCheck{Empty: true}, newSettings(t, "first")); err != nil {
		t.Fatalf("first-time set with a failing login lookup = %v, want nil", err)
	}
	if calls != 0 {
		t.Fatalf("first-time set looked the login up %d times, want 0", calls)
	}

	// 已有登录密码、current 为空：这时才读，读取失败原样返回。
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, failing, domain.PasswordCheck{Empty: true}, newSettings(t, "second")); !errors.Is(err, lookupErr) {
		t.Fatalf("reset with a failing login lookup = %v, want the lookup error", err)
	}
	if calls != 1 {
		t.Fatalf("reset looked the login up %d times, want 1", calls)
	}

	// 窗口关闭（默认）：不读授权，直接 ErrPasswordHashInvalid。
	off := newManageFixture(t, &recordingNotices{}, zap.NewNop())
	v := off.account(t, domain.User{Phone: "15550341013", FirstName: "Off"}, "kept")
	calls = 0
	if err := off.svc.UpdateMyLoginPassword(ctx, testAuthKey, v.ID, failing, domain.PasswordCheck{Empty: true}, newSettings(t, "next")); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("empty current with the window off = %v, want ErrPasswordHashInvalid", err)
	}
	if calls != 0 {
		t.Fatalf("window off looked the login up %d times, want 0", calls)
	}
}

// signedInAtLookup 返回一个总是报告 at 的授权创建时间读取函数。
func signedInAtLookup(at time.Time) func(context.Context) (time.Time, error) {
	return func(context.Context) (time.Time, error) { return at, nil }
}

func TestManageLoginPasswordRejectsBots(t *testing.T) {
	f := newManageFixture(t, &recordingNotices{}, zap.NewNop())
	ctx := context.Background()
	bot, err := f.users.Create(ctx, domain.User{FirstName: "Bot", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if _, err := f.svc.GetMyLoginPassword(ctx, testAuthKey, bot.ID); !errors.Is(err, auth.ErrSystemUserLoginForbidden) {
		t.Fatalf("GetMyLoginPassword for a bot = %v, want ErrSystemUserLoginForbidden", err)
	}
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, bot.ID, nil, domain.PasswordCheck{Empty: true}, newSettings(t, "bot")); !errors.Is(err, auth.ErrSystemUserLoginForbidden) {
		t.Fatalf("UpdateMyLoginPassword for a bot = %v, want ErrSystemUserLoginForbidden", err)
	}
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, domain.OfficialSystemUserID, nil, domain.PasswordCheck{Empty: true}, newSettings(t, "system")); !errors.Is(err, auth.ErrSystemUserLoginForbidden) {
		t.Fatalf("UpdateMyLoginPassword for 777000 = %v, want ErrSystemUserLoginForbidden", err)
	}
}

func TestLoginPasswordChangeNoticeComesFrom777000(t *testing.T) {
	notices := &recordingNotices{}
	f := newManageFixture(t, notices, zap.NewNop())
	ctx := context.Background()
	u, err := f.users.Create(ctx, domain.User{Phone: "15550341002", FirstName: "Notified"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 本来就没有登录密码时删除是空操作：成功、但没有改动可通知。
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, nil, domain.PasswordCheck{Empty: true}, domain.PasswordInputSettings{}); err != nil {
		t.Fatalf("removing a missing login password = %v, want nil", err)
	}
	if got := notices.messages(); len(got) != 0 {
		t.Fatalf("removing a missing login password sent %d notices, want 0", len(got))
	}
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, nil, domain.PasswordCheck{Empty: true}, newSettings(t, "set")); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := notices.messages()
	if len(got) != 1 {
		t.Fatalf("notices after set = %d, want 1", len(got))
	}
	n := got[0]
	if n.SenderUserID != domain.OfficialSystemUserID || n.RecipientUserID != u.ID || n.RandomID == 0 {
		t.Fatalf("notice sender=%d recipient=%d random_id=%d, want 777000 → %d with a random id", n.SenderUserID, n.RecipientUserID, n.RandomID, u.ID)
	}
	if !strings.HasPrefix(n.Message, "Login password set.\n") {
		t.Fatalf("notice = %q, want the set notice", n.Message)
	}
	if len(n.Entities) != 1 || n.Entities[0].Type != domain.MessageEntityBold || n.Entities[0].Offset != 0 || n.Entities[0].Length != len("Login password set.") {
		t.Fatalf("notice entities = %+v, want the title in bold", n.Entities)
	}
}

func TestLoginPasswordChangeSurvivesAFailedNotice(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	notices := &recordingNotices{err: errors.New("message store down")}
	f := newManageFixture(t, notices, zap.New(core))
	ctx := context.Background()
	u, err := f.users.Create(ctx, domain.User{Phone: "15550341003", FirstName: "Quiet"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := f.svc.UpdateMyLoginPassword(ctx, testAuthKey, u.ID, nil, domain.PasswordCheck{Empty: true}, newSettings(t, "kept")); err != nil {
		t.Fatalf("set with a failing notice = %v, want nil", err)
	}
	if _, found, err := f.passwords.GetLoginPassword(ctx, u.ID); err != nil || !found {
		t.Fatalf("login password after a failed notice found=%v err=%v, want set", found, err)
	}
	if logs.FilterMessage("login password change notice").Len() != 1 {
		t.Fatalf("warnings = %v, want one login password change notice warning", logs.All())
	}
}
