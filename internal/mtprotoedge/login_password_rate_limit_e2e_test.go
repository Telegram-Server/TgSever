package mtprotoedge

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	loginpasswordapp "telesrv/internal/app/loginpassword"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// 密码登录限流的端到端测试。
//
// 为什么单独起服务端：限流额度按手机号、auth_key、目标用户在同一个服务端上共用，阈值调小后
// TestLoginPasswordEndToEnd 里对同一号码多次 get 的子测试会误触 FLOOD_WAIT。challenge 淘汰
// 要对同一号码 get 很多次，超出这里的手机号额度，所以放进不带限流器的
// TestLoginPasswordChallengeEvictionEndToEnd。
//
// 限流器是一个只计数、不看时间的本地实现：额度用完就一直拒绝，不需要 sleep。

const (
	rateLimitTestPhoneLimit   = 7
	rateLimitTestAuthKeyLimit = 5
	rateLimitTestSRPLimit     = 2
	rateLimitTestUserLimit    = 3
	rateLimitTestRetryAfter   = 1234
)

// loginPasswordRateLimits 是 startLoginPasswordServer 的限流参数：装上计数限流器，
// 并把验证码签发（手机号、auth_key）、SRP 组、目标用户四个维度的阈值配成给定值。
type loginPasswordRateLimits struct {
	phoneLimit   int
	authKeyLimit int
	srpLimit     int
	userLimit    int
	userWindow   time.Duration
}

// userRateLimit 返回业务层的按目标用户限流选项；l 为 nil 时不限流。
func (l *loginPasswordRateLimits) userRateLimit(limiter *countingRateLimiter) loginpasswordapp.Option {
	if l == nil {
		return nil
	}
	return loginpasswordapp.WithUserRateLimit(limiter, l.userLimit, l.userWindow)
}

// apply 把 RPC 层的阈值写进 cfg，并把限流器装进 deps；l 为 nil 时什么都不做。
func (l *loginPasswordRateLimits) apply(cfg *rpc.Config, deps *rpc.Deps, limiter *countingRateLimiter) {
	if l == nil {
		return
	}
	deps.Limiter = limiter
	cfg.AuthCodePhoneRateLimit = l.phoneLimit
	cfg.AuthCodeAuthKeyRateLimit = l.authKeyLimit
	cfg.AuthCodeRateWindow = time.Hour
	cfg.SRPRateLimit = l.srpLimit
	cfg.SRPRateWindow = time.Hour
}

// countingRateLimiter 按 key 累计 cost，超过 limit 就拒绝，retry_after 固定为
// rateLimitTestRetryAfter。不看窗口：测试期间额度不会恢复。
type countingRateLimiter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newCountingRateLimiter() *countingRateLimiter {
	return &countingRateLimiter{counts: map[string]int{}}
}

func (l *countingRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	return l.AllowN(ctx, key, 1, limit, window)
}

func (l *countingRateLimiter) AllowN(_ context.Context, key string, cost, limit int, _ time.Duration) (bool, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key] += cost
	if l.counts[key] > limit {
		return false, rateLimitTestRetryAfter, nil
	}
	return true, 0, nil
}

// wantFloodWait 断言 err 是 FLOOD_WAIT_<rateLimitTestRetryAfter>。
func wantFloodWait(err error) error {
	if err == nil {
		return fmt.Errorf("succeeded, want FLOOD_WAIT_%d", rateLimitTestRetryAfter)
	}
	wait, ok := tgerr.AsFloodWait(err)
	if !ok {
		return fmt.Errorf("err = %v, want FLOOD_WAIT_%d", err, rateLimitTestRetryAfter)
	}
	if wait != rateLimitTestRetryAfter*time.Second {
		return fmt.Errorf("FLOOD_WAIT = %v, want %ds", wait, rateLimitTestRetryAfter)
	}
	return nil
}

func TestLoginPasswordRateLimitEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true, rateLimits: &loginPasswordRateLimits{
		phoneLimit:   rateLimitTestPhoneLimit,
		authKeyLimit: rateLimitTestAuthKeyLimit,
		srpLimit:     rateLimitTestSRPLimit,
		userLimit:    rateLimitTestUserLimit,
		userWindow:   time.Hour,
	}})

	t.Run("getLoginPassword and signUpWithPassword share the sendCode budget per phone", func(t *testing.T) {
		t.Parallel()
		// 额度 7：连接 A 用 4 次、连接 B 用 3 次（其中两次是参数不合法的注册，也照样计数）；
		// 第三个连接上 sendCode、get、注册都被手机号维度挡下。每把 key 都没到 auth_key 额度 5。
		const phone = "+15550330001"
		var valid tg.AccountPasswordInputSettings
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			var err error
			if valid, err = newLoginPasswordSettings(ctx, raw, phone, "budget"); err != nil { // 1
				return err
			}
			if err := sendCode(ctx, raw, phone); err != nil { // 2
				return err
			}
			if err := signUpWithEmail(ctx, raw, phone, valid); err != nil { // 3
				return err
			}
			_, err = raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone}) // 4
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if err := sendCode(ctx, raw, phone); err != nil { // 5
				return err
			}
			if _, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone}); err != nil { // 6
				return err
			}
			return signUpWithEmail(ctx, raw, phone, valid) // 7
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if err := wantFloodWait(sendCode(ctx, raw, phone)); err != nil {
				return fmt.Errorf("auth.sendCode over budget: %w", err)
			}
			_, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err := wantFloodWait(err); err != nil {
				return fmt.Errorf("getLoginPassword over budget: %w", err)
			}
			_, err = raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{PhoneNumber: phone, FirstName: "Late", NewSettings: valid})
			if err := wantFloodWait(err); err != nil {
				return fmt.Errorf("signUpWithPassword over budget: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
		})
	})

	t.Run("getLoginPassword and signUpWithPassword share the sendCode budget per auth key", func(t *testing.T) {
		t.Parallel()
		// 同一把 auth_key 换着号码调用，额度 5 用完后三个方法都被 auth_key 维度挡下。
		phones := []string{"+15550330101", "+15550330102", "+15550330103", "+15550330104", "+15550330105", "+15550330106", "+15550330107", "+15550330108"}
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			valid, err := newLoginPasswordSettings(ctx, raw, phones[0], "per key") // 1
			if err != nil {
				return err
			}
			if err := sendCode(ctx, raw, phones[1]); err != nil { // 2
				return err
			}
			if err := signUpWithEmail(ctx, raw, phones[2], valid); err != nil { // 3
				return err
			}
			if _, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phones[3]}); err != nil { // 4
				return err
			}
			if err := sendCode(ctx, raw, phones[4]); err != nil { // 5
				return err
			}
			_, err = raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phones[5]})
			if err := wantFloodWait(err); err != nil {
				return fmt.Errorf("getLoginPassword over the auth key budget: %w", err)
			}
			if err := wantFloodWait(sendCode(ctx, raw, phones[6])); err != nil {
				return fmt.Errorf("auth.sendCode over the auth key budget: %w", err)
			}
			_, err = raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{PhoneNumber: phones[7], FirstName: "Late", NewSettings: valid})
			if err := wantFloodWait(err); err != nil {
				return fmt.Errorf("signUpWithPassword over the auth key budget: %w", err)
			}
			return nil
		})
	})

	t.Run("signInWithPassword is limited per target user across auth keys and code sign-in still works", func(t *testing.T) {
		t.Parallel()
		// 目标用户额度 3：三把不同的 key 各猜错一次，第四把 key 即使密码正确也被挡下。
		// 手机号额度：注册 2 次 + 4 次 get + 1 次 sendCode = 7，刚好用满。
		const phone, password = "+15550330010", "the right one"
		var self *tg.User
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			var err error
			self, err = signUpWithPassword(ctx, raw, phone, "Target", "", password)
			return err
		})
		for i := 0; i < rateLimitTestUserLimit; i++ {
			srv.run(t, func(ctx context.Context, raw *tg.Client) error {
				_, err := signInWithPassword(ctx, raw, phone, fmt.Sprintf("guess %d", i))
				if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
					return fmt.Errorf("guess %d: %w", i, err)
				}
				return nil
			})
		}
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, password)
			if err := wantFloodWait(err); err != nil {
				return fmt.Errorf("password sign-in over the user budget from a new auth key: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("business call after a rate-limited sign-in: %w", err)
			}
			// 被锁住的只是密码登录：验证码登录照常可用。
			sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
			if err != nil {
				return fmt.Errorf("auth.sendCode while password sign-in is limited: %w", err)
			}
			if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: sent.(*tg.AuthSentCode).PhoneCodeHash, PhoneCode: loginPasswordTestCode}); err != nil {
				return fmt.Errorf("auth.signIn with the code while password sign-in is limited: %w", err)
			}
			full, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err != nil {
				return fmt.Errorf("users.getFullUser after code sign-in: %w", err)
			}
			if full.FullUser.ID != self.ID {
				return fmt.Errorf("getFullUser id = %d, want %d", full.FullUser.ID, self.ID)
			}
			return nil
		})
	})

	t.Run("signInWithPassword is in the SRP group per auth key", func(t *testing.T) {
		t.Parallel()
		// SRP 组额度 2：同一把 key 第三次提交被挡下；被 SRP 组挡下的那次不消耗目标用户额度，
		// 所以换一把 key 后第 3 次针对该用户的尝试（额度 3 以内）登录成功。
		const phone, password = "+15550330020", "srp group"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Group", "", password)
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			for i := 0; i < rateLimitTestSRPLimit; i++ {
				_, err := signInWithPassword(ctx, raw, phone, fmt.Sprintf("guess %d", i))
				if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
					return fmt.Errorf("guess %d: %w", i, err)
				}
			}
			_, err := signInWithPassword(ctx, raw, phone, password)
			if err := wantFloodWait(err); err != nil {
				return fmt.Errorf("third submission on one auth key: %w", err)
			}
			return nil
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signInWithPassword(ctx, raw, phone, password); err != nil {
				return fmt.Errorf("sign-in from a fresh auth key: %w", err)
			}
			_, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})
}

func TestLoginPasswordRateLimitDisabledDimensionsEndToEnd(t *testing.T) {
	t.Parallel()
	// 限流器装着，但四个维度都配成 <=0：每个维度都不再生效。
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true, rateLimits: &loginPasswordRateLimits{
		phoneLimit:   -1,
		authKeyLimit: 0,
		srpLimit:     0,
		userLimit:    0,
		userWindow:   time.Hour,
	}})
	const phone, password = "+15550330040", "unlimited"
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		_, err := signUpWithPassword(ctx, raw, phone, "Free", "", password)
		return err
	})
	// 同一把 key、同一个号码、同一个用户，次数超过上面测试里每个维度的额度。
	const attempts = rateLimitTestPhoneLimit + 1
	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		for i := 0; i < attempts; i++ {
			_, err := signInWithPassword(ctx, raw, phone, fmt.Sprintf("guess %d", i))
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("guess %d: %w", i, err)
			}
		}
		if err := sendCode(ctx, raw, phone); err != nil {
			return fmt.Errorf("auth.sendCode: %w", err)
		}
		if _, err := signInWithPassword(ctx, raw, phone, password); err != nil {
			return fmt.Errorf("sign-in with the right password: %w", err)
		}
		_, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		return err
	})
}

func TestLoginPasswordChallengeEvictionEndToEnd(t *testing.T) {
	t.Parallel()

	t.Run("challenges beyond the per auth key cap evict the oldest", func(t *testing.T) {
		t.Parallel()
		srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true})
		const phone, password = "+15550330030", "evicted"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Evict", "", password)
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			// 每个 auth_key 最多挂 5 个 challenge：第 6 个挤掉第 1 个。
			var pws []*tg.AccountPassword
			for i := 0; i < 6; i++ {
				pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
				if err != nil {
					return err
				}
				pws = append(pws, pw)
			}
			oldest, err := loginPasswordProof(pws[0], password)
			if err != nil {
				return err
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: oldest})
			if err := wantRPCError(err, 400, "SRP_ID_INVALID"); err != nil {
				return fmt.Errorf("evicted challenge: %w", err)
			}
			next, err := loginPasswordProof(pws[1], password)
			if err != nil {
				return err
			}
			if _, err := raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: next}); err != nil {
				return fmt.Errorf("second-oldest challenge: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})

	t.Run("challenges beyond the global cap evict the oldest across auth keys", func(t *testing.T) {
		t.Parallel()
		// 全局上限调成 2：三把不同的 key 各取一个 challenge，第一把 key 的被挤掉。
		srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true,
			challengeLimits: []memory.LoginPasswordChallengeOption{memory.WithLoginPasswordChallengeLimits(5, 2)}})
		const phone, password = "+15550330031", "global cap"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Global", "", password)
			return err
		})
		srv.run(t, func(ctx context.Context, first *tg.Client) error {
			pw, err := first.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			proof, err := loginPasswordProof(pw, password)
			if err != nil {
				return err
			}
			for i := 0; i < 2; i++ {
				if err := srv.runWithUpdates(t, nil, func(ctx context.Context, other *tg.Client) error {
					_, err := other.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
					return err
				}); err != nil {
					return fmt.Errorf("other connection %d: %w", i, err)
				}
			}
			_, err = first.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: proof})
			return wantRPCError(err, 400, "SRP_ID_INVALID")
		})
	})
}

// sendCode 调用 auth.sendCode，只关心错误。
func sendCode(ctx context.Context, raw *tg.Client, phone string) error {
	_, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
	return err
}

// signUpWithEmail 提交一个带 email 的注册（登录密码不接受 email，必然 NEW_SETTINGS_INVALID），
// 用来消耗额度而不建号。返回 nil 表示得到了预期的 NEW_SETTINGS_INVALID。
func signUpWithEmail(ctx context.Context, raw *tg.Client, phone string, settings tg.AccountPasswordInputSettings) error {
	settings.SetEmail("someone@example.test")
	_, err := raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{PhoneNumber: phone, FirstName: "Mail", NewSettings: settings})
	if err := wantRPCError(err, 400, "NEW_SETTINGS_INVALID"); err != nil {
		return fmt.Errorf("signUpWithPassword with an email: %w", err)
	}
	return nil
}
