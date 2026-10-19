package mtprotoedge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 已登录时管理登录密码（telesrv.getMyLoginPassword / telesrv.updateLoginPassword）的端到端
// 子测试。它们挂在 TestLoginPasswordEndToEnd 下，复用那里只建一次的共享服务端；每个场景
// 用 +1555034xxxx 段里自己的号码隔离，并行运行，不 sleep。客户端一侧的 SRP 计算与
// 登录、注册的子测试相同，直接用 td 的 crypto/srp（loginPasswordProof、loginPasswordSettingsFor）。

// 登录密码改动通知的标题行（777000 消息正文的第一行）。
const (
	loginPasswordSetNotice     = "Login password set."
	loginPasswordChangedNotice = "Login password changed."
	loginPasswordRemovedNotice = "Login password removed."
)

func runLoginPasswordManageSubtests(t *testing.T, srv *loginPasswordServer) {
	t.Run("getMyLoginPassword without a login password lets a code account set one and sign in with it", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550340010", "first password"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithCode(ctx, raw, phone, "Coded"); err != nil {
				return err
			}
			pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return fmt.Errorf("getMyLoginPassword: %w", err)
			}
			if pw.HasPassword {
				return fmt.Errorf("has_password = true for a code-registered account, want false")
			}
			if _, ok := pw.GetCurrentAlgo(); ok {
				return fmt.Errorf("current_algo present without a login password")
			}
			if _, err := registrationAlgo(pw); err != nil {
				return err
			}
			if err := setMyLoginPassword(ctx, raw, password); err != nil {
				return err
			}
			pw, err = raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return fmt.Errorf("getMyLoginPassword after setting: %w", err)
			}
			if !pw.HasPassword || len(pw.SRPB) != 256 || pw.SRPID == 0 {
				return fmt.Errorf("getMyLoginPassword after setting = has_password %v, srp_B %d bytes, srp_id %d; want a complete SRP challenge",
					pw.HasPassword, len(pw.SRPB), pw.SRPID)
			}
			return nil
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signInWithPassword(ctx, raw, phone, password); err != nil {
				return fmt.Errorf("signInWithPassword with the new login password: %w", err)
			}
			_, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})

	t.Run("updateLoginPassword with a login password set requires the current one", func(t *testing.T) {
		t.Parallel()
		const phone, oldPassword, newPassword = "+15550340020", "old password", "new password"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithPassword(ctx, raw, phone, "Change", "", oldPassword); err != nil {
				return err
			}
			// current 为空：账号已有登录密码时不允许。
			pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			settings, err := newSettingsFromMine(pw, newPassword)
			if err != nil {
				return err
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: settings})
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("empty current: %w", err)
			}
			// current 是错误的密码。
			wrong, err := loginPasswordProof(pw, "a guess")
			if err != nil {
				return err
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: wrong, NewSettings: settings})
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("wrong current: %w", err)
			}
			// 校验失败的 challenge 已作废，再交一次（即使证明正确）也不行。
			right, err := loginPasswordProof(pw, oldPassword)
			if err != nil {
				return err
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: right, NewSettings: settings})
			if err := wantRPCError(err, 400, "SRP_ID_INVALID"); err != nil {
				return fmt.Errorf("resubmitting a burned challenge: %w", err)
			}
			// current 正确：修改成功。
			return changeMyLoginPassword(ctx, raw, oldPassword, newPassword)
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, oldPassword)
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("signing in with the old password: %w", err)
			}
			if _, err := signInWithPassword(ctx, raw, phone, newPassword); err != nil {
				return fmt.Errorf("signing in with the new password: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})

	t.Run("updateLoginPassword with an empty new hash removes the login password", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550340030", "short lived"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithPassword(ctx, raw, phone, "Remove", "", password); err != nil {
				return err
			}
			if err := removeMyLoginPassword(ctx, raw, password); err != nil {
				return err
			}
			pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			if pw.HasPassword {
				return fmt.Errorf("getMyLoginPassword after removal: has_password = true")
			}
			return nil
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			return wantRPCError(err, 400, "PASSWORD_LOGIN_NOT_SET")
		})
	})

	t.Run("updateLoginPassword rejects an email and a malformed new salt", func(t *testing.T) {
		t.Parallel()
		const phone = "+15550340040"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithCode(ctx, raw, phone, "Invalid"); err != nil {
				return err
			}
			pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			withEmail, err := newSettingsFromMine(pw, "with email")
			if err != nil {
				return err
			}
			withEmail.SetEmail("someone@example.test")
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: withEmail})
			if err := wantRPCError(err, 400, "NEW_SETTINGS_INVALID"); err != nil {
				return fmt.Errorf("email: %w", err)
			}
			shortSalt, err := newSettingsFromMine(pw, "short salt")
			if err != nil {
				return err
			}
			algo := shortSalt.NewAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
			algo.Salt1 = algo.Salt1[:len(algo.Salt1)-32]
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: shortSalt})
			if err := wantRPCError(err, 400, "NEW_SALT_INVALID"); err != nil {
				return fmt.Errorf("salt: %w", err)
			}
			// 被拒绝的请求没有留下密码。
			pw, err = raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			if pw.HasPassword {
				return fmt.Errorf("has_password after rejected updates = true")
			}
			return nil
		})
	})

	t.Run("every login password change notifies the account's other sessions from 777000", func(t *testing.T) {
		t.Parallel()
		const phone = "+15550340050"
		// 会话 A：验证码注册后保持在线，收集 777000 发来的新消息。
		notices := make(chan string, 16)
		handler := telegram.UpdateHandlerFunc(func(_ context.Context, u tg.UpdatesClass) error {
			for _, text := range officialMessagesFromUpdates(u) {
				if strings.Contains(text, "Login password") {
					select {
					case notices <- text:
					default:
					}
				}
			}
			return nil
		})
		online := make(chan struct{})
		release := make(chan struct{})
		sessionA := make(chan error, 1)
		go func() {
			sessionA <- srv.runWithUpdates(t, handler, func(ctx context.Context, raw *tg.Client) error {
				if _, err := signUpWithCode(ctx, raw, phone, "Watcher"); err != nil {
					return err
				}
				if _, err := raw.UpdatesGetState(ctx); err != nil {
					return err
				}
				close(online)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		select {
		case <-online:
		case err := <-sessionA:
			t.Fatalf("session A: %v", err)
		}
		// waitNotice 在 client 的回调里调用，所以失败时返回 error 而不是 t.Fatal。
		waitNotice := func(want string) error {
			select {
			case text := <-notices:
				if !strings.HasPrefix(text, want) {
					return fmt.Errorf("777000 notice pushed to session A = %q, want one starting with %q", text, want)
				}
				return nil
			case err := <-sessionA:
				return fmt.Errorf("session A ended before the %q notice: %v", want, err)
			case <-srv.ctx.Done():
				return fmt.Errorf("session A received no %q notice", want)
			}
		}

		// 会话 B：同一账号用验证码登录，依次设置、修改、删除登录密码。
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if err := signInWithCode(ctx, raw, phone); err != nil {
				return err
			}
			steps := []struct {
				want   string
				change func() error
			}{
				{loginPasswordSetNotice, func() error { return setMyLoginPassword(ctx, raw, "one") }},
				{loginPasswordChangedNotice, func() error { return changeMyLoginPassword(ctx, raw, "one", "two") }},
				{loginPasswordRemovedNotice, func() error { return removeMyLoginPassword(ctx, raw, "two") }},
			}
			for _, step := range steps {
				if err := step.change(); err != nil {
					return err
				}
				if err := waitNotice(step.want); err != nil {
					return err
				}
				// 通知是落库的 777000 消息：做改动的会话自己的对话列表里也有这一条。
				top, err := latestOfficialMessage(ctx, raw)
				if err != nil {
					return err
				}
				if !strings.HasPrefix(top, step.want) {
					return fmt.Errorf("777000 top message after the change = %q, want one starting with %q", top, step.want)
				}
			}
			return nil
		})
		close(release)
		if err := <-sessionA; err != nil {
			t.Fatalf("session A: %v", err)
		}
	})

	t.Run("challenges issued before a password change return SRP_PASSWORD_CHANGED", func(t *testing.T) {
		t.Parallel()
		const phone, oldPassword, newPassword = "+15550340060", "before", "after"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithPassword(ctx, raw, phone, "Stale", "", oldPassword); err != nil {
				return err
			}
			// 先拿一个管理用的 challenge，随后被一次修改抢先。
			stalePw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			staleProof, err := loginPasswordProof(stalePw, oldPassword)
			if err != nil {
				return err
			}
			if err := changeMyLoginPassword(ctx, raw, oldPassword, newPassword); err != nil {
				return err
			}
			settings, err := newSettingsFromMine(stalePw, "third")
			if err != nil {
				return err
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: staleProof, NewSettings: settings})
			if err := wantRPCError(err, 400, "SRP_PASSWORD_CHANGED"); err != nil {
				return fmt.Errorf("updateLoginPassword with a stale challenge: %w", err)
			}
			return nil
		})
		// 未登录的连接先拿到登录 challenge 并算好证明，密码随后在另一个会话里被改掉。
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			proof, err := loginPasswordProof(pw, newPassword)
			if err != nil {
				return err
			}
			changeErr := srv.runWithUpdates(t, nil, func(ctx context.Context, other *tg.Client) error {
				if _, err := signInWithPassword(ctx, other, phone, newPassword); err != nil {
					return err
				}
				return changeMyLoginPassword(ctx, other, newPassword, "fourth")
			})
			if changeErr != nil {
				return fmt.Errorf("changing the password from another session: %w", changeErr)
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: proof})
			if err := wantRPCError(err, 400, "SRP_PASSWORD_CHANGED"); err != nil {
				return fmt.Errorf("signInWithPassword with a challenge from before the change: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
		})
	})

	t.Run("deleting the account deletes its login password", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550340070", "gone with the account"
		var deletedID int64
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			self, err := signUpWithPassword(ctx, raw, phone, "Leaving", "", password)
			if err != nil {
				return err
			}
			deletedID = self.ID
			ok, err := raw.AccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: "testing"})
			if err != nil || !ok {
				return fmt.Errorf("account.deleteAccount = %v, %v", ok, err)
			}
			return nil
		})
		if _, found, err := srv.loginPasswords.GetLoginPassword(srv.ctx, deletedID); err != nil || found {
			t.Fatalf("login password of the deleted account found=%v err=%v, want gone", found, err)
		}
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			// 号码不再属于任何账号：getLoginPassword 返回注册参数。
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return fmt.Errorf("getLoginPassword after deletion: %w", err)
			}
			if pw.HasPassword {
				return fmt.Errorf("getLoginPassword after deletion: has_password = true")
			}
			// 用同一个号码重新注册的新账号没有旧的登录密码。
			if _, err := signUpWithCode(ctx, raw, phone, "Again"); err != nil {
				return err
			}
			mine, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err != nil {
				return err
			}
			if mine.HasPassword {
				return fmt.Errorf("re-registered account inherited a login password")
			}
			_, err = signInWithPassword(ctx, raw, phone, password)
			return wantRPCError(err, 400, "PASSWORD_LOGIN_NOT_SET")
		})
	})

	t.Run("unauthorized and password_pending sessions cannot manage the login password", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("getMyLoginPassword before login: %w", err)
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}})
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("updateLoginPassword before login: %w", err)
			}
			return nil
		})

		const phone, loginPassword, cloudPassword = "+15550340080", "login password", "cloud password"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithPassword(ctx, raw, phone, "Pending", "", loginPassword); err != nil {
				return err
			}
			return enableCloudPassword(ctx, raw, cloudPassword)
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, loginPassword)
			if err := wantRPCError(err, 401, "SESSION_PASSWORD_NEEDED"); err != nil {
				return fmt.Errorf("signInWithPassword: %w", err)
			}
			_, err = raw.TelesrvGetMyLoginPassword(ctx, 0)
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("getMyLoginPassword while password_pending: %w", err)
			}
			_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}})
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("updateLoginPassword while password_pending: %w", err)
			}
			return nil
		})
		// 被拒绝的调用没有动到密码：原来的登录密码仍然有效（仍需 2FA）。
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, loginPassword)
			return wantRPCError(err, 401, "SESSION_PASSWORD_NEEDED")
		})
	})
}

// runLoginPasswordManageDisabled 在开关关闭的服务端上验证：已登录的会话调用两个管理方法
// 也只会得到 PASSWORD_LOGIN_DISABLED，参数内容无关紧要。
func runLoginPasswordManageDisabled(t *testing.T, srv *loginPasswordServer) {
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		if _, err := signUpWithCode(ctx, raw, "+15550349010", "Off"); err != nil {
			return err
		}
		_, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
		if err := wantRPCError(err, 403, "PASSWORD_LOGIN_DISABLED"); err != nil {
			return fmt.Errorf("getMyLoginPassword: %w", err)
		}
		var unknown tg.AccountPasswordInputSettings
		unknown.SetNewAlgo(&tg.PasswordKdfAlgoUnknown{})
		unknown.SetNewPasswordHash([]byte{1})
		_, err = raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: unknown})
		if err := wantRPCError(err, 403, "PASSWORD_LOGIN_DISABLED"); err != nil {
			return fmt.Errorf("updateLoginPassword: %w", err)
		}
		return nil
	})
}

// newSettingsFromMine 用 getMyLoginPassword 返回的 new_algo 为 password 算出 new_settings。
func newSettingsFromMine(pw *tg.AccountPassword, password string) (tg.AccountPasswordInputSettings, error) {
	algo, err := registrationAlgo(pw)
	if err != nil {
		return tg.AccountPasswordInputSettings{}, err
	}
	return loginPasswordSettingsFor(algo, password)
}

// setMyLoginPassword 给还没有登录密码的已登录账号设置登录密码（current 为空）。
func setMyLoginPassword(ctx context.Context, raw *tg.Client, password string) error {
	pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
	if err != nil {
		return fmt.Errorf("getMyLoginPassword: %w", err)
	}
	settings, err := newSettingsFromMine(pw, password)
	if err != nil {
		return err
	}
	return wantUpdated(raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: &tg.InputCheckPasswordEmpty{}, NewSettings: settings}))
}

// changeMyLoginPassword 用当前密码的 SRP 证明把登录密码从 oldPassword 改成 newPassword。
func changeMyLoginPassword(ctx context.Context, raw *tg.Client, oldPassword, newPassword string) error {
	pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
	if err != nil {
		return fmt.Errorf("getMyLoginPassword: %w", err)
	}
	current, err := loginPasswordProof(pw, oldPassword)
	if err != nil {
		return err
	}
	settings, err := newSettingsFromMine(pw, newPassword)
	if err != nil {
		return err
	}
	return wantUpdated(raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: current, NewSettings: settings}))
}

// removeMyLoginPassword 按客户端删除密码的方式（new_algo 为 unknown、new_password_hash 为空）
// 删除登录密码。
func removeMyLoginPassword(ctx context.Context, raw *tg.Client, password string) error {
	pw, err := raw.TelesrvGetMyLoginPassword(ctx, 0)
	if err != nil {
		return fmt.Errorf("getMyLoginPassword: %w", err)
	}
	current, err := loginPasswordProof(pw, password)
	if err != nil {
		return err
	}
	var settings tg.AccountPasswordInputSettings
	settings.SetNewAlgo(&tg.PasswordKdfAlgoUnknown{})
	settings.SetNewPasswordHash([]byte{})
	settings.SetHint("")
	return wantUpdated(raw.TelesrvUpdateLoginPassword(ctx, &tg.TelesrvUpdateLoginPasswordRequest{Current: current, NewSettings: settings}))
}

func wantUpdated(ok bool, err error) error {
	if err != nil {
		return fmt.Errorf("updateLoginPassword: %w", err)
	}
	if !ok {
		return fmt.Errorf("updateLoginPassword = false, want true")
	}
	return nil
}

// enableCloudPassword 在已登录会话里开启 2FA 云密码。
func enableCloudPassword(ctx context.Context, raw *tg.Client, password string) error {
	current, err := raw.AccountGetPassword(ctx)
	if err != nil {
		return fmt.Errorf("account.getPassword: %w", err)
	}
	algo, err := registrationAlgo(current)
	if err != nil {
		return err
	}
	settings, err := loginPasswordSettingsFor(algo, password)
	if err != nil {
		return err
	}
	ok, err := raw.AccountUpdatePasswordSettings(ctx, &tg.AccountUpdatePasswordSettingsRequest{
		Password:    &tg.InputCheckPasswordEmpty{},
		NewSettings: settings,
	})
	if err != nil || !ok {
		return fmt.Errorf("account.updatePasswordSettings = %v, %v", ok, err)
	}
	return nil
}

// signInWithCode 用验证码（开发固定码）登录一个已存在的账号。
func signInWithCode(ctx context.Context, raw *tg.Client, phone string) error {
	sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
	if err != nil {
		return fmt.Errorf("auth.sendCode: %w", err)
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		return fmt.Errorf("auth.sendCode = %T, want *tg.AuthSentCode", sent)
	}
	res, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: code.PhoneCodeHash, PhoneCode: loginPasswordTestCode})
	if err != nil {
		return fmt.Errorf("auth.signIn: %w", err)
	}
	if _, ok := res.(*tg.AuthAuthorization); !ok {
		return fmt.Errorf("auth.signIn = %T, want *tg.AuthAuthorization", res)
	}
	return nil
}

// officialMessagesFromUpdates 取出一批推送里系统账号 777000 发来的新私聊消息正文。
func officialMessagesFromUpdates(u tg.UpdatesClass) []string {
	if short, ok := u.(*tg.UpdateShortMessage); ok {
		if short.UserID == domain.OfficialSystemUserID && !short.Out {
			return []string{short.Message}
		}
		return nil
	}
	var out []string
	for _, upd := range updatesFromClass(u) {
		newMessage, ok := upd.(*tg.UpdateNewMessage)
		if !ok {
			continue
		}
		msg, ok := newMessage.Message.(*tg.Message)
		if !ok || msg.Out {
			continue
		}
		if peer, ok := msg.PeerID.(*tg.PeerUser); ok && peer.UserID == domain.OfficialSystemUserID {
			out = append(out, msg.Message)
		}
	}
	return out
}
