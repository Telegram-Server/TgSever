package mtprotoedge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

// loginPasswordResetNotice 是免旧密码重设后 777000 通知的标题行，与普通修改的
// loginPasswordChangedNotice 不同。
const loginPasswordResetNotice = "Login password reset."

// 忘记登录密码（#35）的端到端测试：先用验证码登录，在重设窗口内 telesrv.updateLoginPassword
// 的 current 可以为空。窗口按当前授权的创建时间算，每次登录都会刷新。
//
// 为什么单独起服务端：重设窗口是服务端级别的配置，TestLoginPasswordEndToEnd 的共享服务端
// 不开窗口（那里"刚注册就用空 current 修改"必须被拒绝）。这里一个服务端开 10 分钟窗口，
// 另一个把窗口配成 0，用来确认关闭后空 current 一律被拒绝；都不 sleep。号码用 +1555035xxxx 段。

func TestLoginPasswordResetEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true, resetWindow: 10 * time.Minute})
	closed := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true, resetWindow: 0})

	t.Run("within the reset window an empty current resets a forgotten login password", func(t *testing.T) {
		t.Parallel()
		const phone, forgotten, reset = "+15550350010", "forgotten password", "reset password"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Forgetful", "", forgotten)
			return err
		})
		// 未登录的连接在重设之前拿到登录 challenge 并算好证明（假设密码在别处还记得），
		// 重设之后再交：与普通修改一样，旧 challenge 失效。
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			loginPw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			loginProof, err := loginPasswordProof(loginPw, forgotten)
			if err != nil {
				return err
			}
			// 另一个会话：忘了密码，用验证码登录，随即不填旧密码重设。
			resetErr := srv.runWithUpdates(t, nil, func(ctx context.Context, other *tg.Client) error {
				return resetForgottenLoginPassword(ctx, other, phone, forgotten, reset)
			})
			if resetErr != nil {
				return resetErr
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: loginProof})
			if err := wantRPCError(err, 400, "SRP_PASSWORD_CHANGED"); err != nil {
				return fmt.Errorf("login challenge issued before the reset: %w", err)
			}
			return nil
		})
		// 重设之后：旧密码不能登录，新密码可以。
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, forgotten)
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("signing in with the forgotten password: %w", err)
			}
			if _, err := signInWithPassword(ctx, raw, phone, reset); err != nil {
				return fmt.Errorf("signing in with the reset password: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})

	t.Run("with the reset window set to 0 an empty current is rejected", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550350020", "kept password"
		closed.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Closed", "", password)
			return err
		})
		closed.run(t, func(ctx context.Context, raw *tg.Client) error {
			if err := signInWithCode(ctx, raw, phone); err != nil {
				return err
			}
			pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			settings, err := newSettingsFromMine(pw, "not allowed")
			if err != nil {
				return err
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: settings})
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("empty current with the reset window closed: %w", err)
			}
			return nil
		})
		// 被拒绝的重设没有动到密码。
		closed.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, password)
			return err
		})
	})
}

// resetForgottenLoginPassword 按忘记密码的流程操作：验证码登录后不填旧密码，把登录密码重设为
// reset。它还确认 777000 发的是区别于普通修改的"免旧密码重设"通知，并且这个会话在重设之前拿到的管理用 challenge
// （按 forgotten 算出证明）随之失效。
func resetForgottenLoginPassword(ctx context.Context, raw *tg.Client, phone, forgotten, reset string) error {
	if err := signInWithCode(ctx, raw, phone); err != nil {
		return err
	}
	pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
	if err != nil {
		return err
	}
	if !pw.HasPassword {
		return fmt.Errorf("getMyLoginPassword before the reset: has_password = false")
	}
	ownProof, err := loginPasswordProof(pw, forgotten)
	if err != nil {
		return err
	}
	settings, err := newSettingsFromMine(pw, reset)
	if err != nil {
		return err
	}
	if err := wantUpdated(raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: settings})); err != nil {
		return fmt.Errorf("reset with an empty current: %w", err)
	}
	top, err := latestOfficialMessage(ctx, raw)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(top, loginPasswordResetNotice+"\n") || !strings.Contains(top, "without the old password") ||
		!strings.Contains(top, "active sessions") {
		return fmt.Errorf("777000 top message after the reset = %q, want the %q notice that names a reset without the old password and points at active sessions", top, loginPasswordResetNotice)
	}
	again, err := newSettingsFromMine(pw, "third")
	if err != nil {
		return err
	}
	_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: ownProof, NewSettings: again})
	if err := wantRPCError(err, 400, "SRP_PASSWORD_CHANGED"); err != nil {
		return fmt.Errorf("management challenge issued before the reset: %w", err)
	}
	return nil
}
