package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// 设置登录密码并撤销会话（revoke_sessions）时的部分失败与请求取消：密码一旦写入就不能回滚，
// 命令记录必须如实反映"密码已设置"，否则运营者换 command id 重试会二次重设、二次通知。

// errRevokeFailed 模拟撤销会话时关闭连接失败。
var errRevokeFailed = errors.New("revoke auth key: connection registry unavailable")

// scriptedRevoker 在撤销时先执行 onRevoke（例如取消请求），记下当时 context 的状态，再返回 err。
type scriptedRevoker struct {
	err      error
	onRevoke func()
	ctxErr   error
}

func (r *scriptedRevoker) RevokeAuthorizationAuthKey(ctx context.Context, _ [8]byte, _ int64) error {
	if r.onRevoke != nil {
		r.onRevoke()
	}
	r.ctxErr = ctx.Err()
	return r.err
}

// withRevokerAndRepo 用指定的撤销器与命令存储重建 fixture 的后台服务（登录密码服务不变）。
func (f *loginPasswordFixture) withRevokerAndRepo(revoker AuthKeyRevoker, repo CommandRepository) {
	f.svc = NewService(Dependencies{
		Commands:                    repo,
		Auth:                        f.auth,
		Revoker:                     revoker,
		LoginPasswords:              f.svc.loginPasswords,
		LoginPasswordFingerprintKey: DeriveLoginPasswordFingerprintKey("admin-api-token"),
		Now:                         fixedNow,
	})
}

// 密码已写入但撤销会话失败：命令记为成功（密码确实设置了），结果里标明撤销失败、请另行撤销；
// 同一 command id 重放不会二次重设、二次通知。
func TestSetLoginPasswordRevokeFailureIsRecordedAsSucceededWithAWarning(t *testing.T) {
	f := newLoginPasswordFixture(t)
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550088021", FirstName: "Partial"})
	f.auth.items = []domain.Authorization{{AuthKeyID: [8]byte{3}, UserID: u.ID, Hash: 103}}
	f.withRevokerAndRepo(&scriptedRevoker{err: errRevokeFailed}, f.repo)

	req := setLoginPasswordRequest("exec-set-pw-revoke-fail", u.ID, "fresh")
	req.RevokeSessions = true
	res, err := f.svc.SetLoginPassword(ctx, req)
	if err != nil {
		t.Fatalf("SetLoginPassword err=%v, want success with a revoke warning (result=%+v)", err, res)
	}
	if res.Status != string(domain.AdminCommandCompleted) || f.repo.items[req.CommandID].Status != domain.AdminCommandCompleted {
		t.Fatalf("status=%s recorded=%s, want completed", res.Status, f.repo.items[req.CommandID].Status)
	}
	if res.Details["revoke_sessions_failed"] != true || !strings.Contains(fmt.Sprint(res.Details["revoke_sessions_error"]), "connection registry unavailable") {
		t.Fatalf("details=%+v, want revoke_sessions_failed=true with the error", res.Details)
	}
	if !strings.Contains(res.Message, "revoking sessions failed") {
		t.Fatalf("message=%q, want it to say revoking sessions failed", res.Message)
	}
	if err := f.signIn(t, "+15550088021", f.challenge(t, "+15550088021"), "fresh"); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	notices := len(f.notices.messages())

	replay, err := f.svc.SetLoginPassword(ctx, req)
	if err != nil || !replay.AlreadyExecuted {
		t.Fatalf("replay result=%+v err=%v, want already executed", replay, err)
	}
	if got := len(f.notices.messages()); got != notices {
		t.Fatalf("replay sent %d more notices, want none", got-notices)
	}
}

// 请求在命令执行中被取消（后台的请求被掐断）时，命令仍按不跟随取消的 context 执行完并记录，
// 不会停在 running。
func TestSetLoginPasswordFinishesAfterCancellation(t *testing.T) {
	f := newLoginPasswordFixture(t)
	u := f.user(t, domain.User{Phone: "15550088022", FirstName: "Cut"})
	f.auth.items = []domain.Authorization{{AuthKeyID: [8]byte{4}, UserID: u.ID, Hash: 104}}
	repo := &ctxCheckingCommandRepo{memoryCommandRepo: newMemoryCommandRepo()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	revoker := &scriptedRevoker{onRevoke: cancel}
	f.withRevokerAndRepo(revoker, repo)

	req := setLoginPasswordRequest("exec-set-pw-cancelled", u.ID, "fresh")
	req.RevokeSessions = true
	res, err := f.svc.SetLoginPassword(ctx, req)
	if err != nil || res.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("result=%+v err=%v, want completed", res, err)
	}
	if revoker.ctxErr != nil {
		t.Fatalf("revoke saw a cancelled context: %v", revoker.ctxErr)
	}
	if repo.items[req.CommandID].Status != domain.AdminCommandCompleted {
		t.Fatalf("recorded %+v, want the command finished", repo.items[req.CommandID])
	}
}
