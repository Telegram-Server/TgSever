package mtprotoedge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/clock"
	tdsrp "github.com/iamxvbaba/td/crypto/srp"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/app/account"
	"telesrv/internal/app/auth"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/dialogs"
	"telesrv/internal/app/help"
	loginpasswordapp "telesrv/internal/app/loginpassword"
	messageapp "telesrv/internal/app/messages"
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// 手机号 + 密码登录（telesrv 扩展协议，#27）的端到端测试：真实 td 客户端经 TCP、
// 握手、edge、rpc、业务层，落到内存 store。客户端一侧的 SRP 计算直接用 td 的
// crypto/srp，证明真实客户端算出的值服务端能接受。
//
// 结构：TestLoginPasswordEndToEnd 只建一次开关打开的服务端（loginPasswordServer），
// 每个场景是一个并行子测试、用自己的手机号隔离。#31 注册、#32 登录已在这里；后续票
// （#33 限流、#34 修改密码）往这里加子测试即可；#35 忘记密码的重设窗口是服务端级别配置，
// 在 login_password_reset_e2e_test.go 里单独起服务端。需要调小阈值或窗口时，
// 在 loginPasswordServerOptions 里加字段，由 startLoginPasswordServer 传给业务层。
// 服务端带 SessionManager（观察新设备登录推送），2FA 云密码 store 由 auth 和 account 共用。
//
// 开关关闭是服务端级别的配置，同一个服务端不能同时处于两种状态，所以放在
// TestLoginPasswordDisabledEndToEnd 里单独起一个服务端；两个测试共用同一把 RSA key。

const (
	loginPasswordTestDC   = 2
	loginPasswordTestCode = "12345"
)

var (
	loginPasswordRSAOnce sync.Once
	loginPasswordRSA     *rsa.PrivateKey
	loginPasswordRSAErr  error
)

// loginPasswordRSAKey 在本包的密码登录测试之间共享一把 RSA key，只生成一次。
func loginPasswordRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	loginPasswordRSAOnce.Do(func() {
		loginPasswordRSA, loginPasswordRSAErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if loginPasswordRSAErr != nil {
		t.Fatalf("gen rsa: %v", loginPasswordRSAErr)
	}
	return loginPasswordRSA
}

// loginPasswordServerOptions 是密码登录服务端的可调参数。
type loginPasswordServerOptions struct {
	enabled bool
	// rateLimits 不为 nil 时装上计数限流器并按它配置各维度阈值（见 login_password_rate_limit_e2e_test.go）。
	rateLimits *loginPasswordRateLimits
	// challengeLimits 传给内存 challenge store，用来调小挂起 challenge 的上限。
	challengeLimits []memory.LoginPasswordChallengeOption
	// resetWindow 是忘记密码免旧密码重设的窗口（见 login_password_reset_e2e_test.go）；
	// 零值关闭，已有登录密码时一律要求 current。
	resetWindow time.Duration
	// signUpDisabled 关闭注册（TELESRV_SIGN_UP_ENABLED=false），见 sign_up_switch_e2e_test.go。
	// 零值保持注册开放。
	signUpDisabled bool
}

// loginPasswordServer 是一个跑在随机端口上的完整服务端，供多个子测试并发使用。
type loginPasswordServer struct {
	rsaKey *rsa.PrivateKey
	addr   *net.TCPAddr
	ctx    context.Context
	users  *tombstoningUserStore
	// loginPasswords 是服务端用的登录密码存储，供测试直接确认某一行已经不在了。
	loginPasswords *memory.LoginPasswordStore
	// auth 是服务端用的 auth 服务，供后台建号（admin.AccountCreator）在进程内接上。
	auth *auth.Service
}

// tombstoningUserStore 在内存 UserStore 外面加一层"已注销"标记。内存 store 没有账号
// 注销的实现（注销由 postgres 的 AccountLifecycleStore 完成），端到端测试用它模拟
// 注销后的账号：ByID 返回 Deleted=true 的墓碑，ByPhone 不再找到它，与 postgres 一致。
type tombstoningUserStore struct {
	*memory.UserStore
	mu      sync.Mutex
	deleted map[int64]bool
}

func (s *tombstoningUserStore) tombstone(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted[userID] = true
}

func (s *tombstoningUserStore) isDeleted(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleted[userID]
}

func (s *tombstoningUserStore) ByID(ctx context.Context, id int64) (domain.User, bool, error) {
	u, found, err := s.UserStore.ByID(ctx, id)
	if found && s.isDeleted(id) {
		u.Deleted = true
	}
	return u, found, err
}

func (s *tombstoningUserStore) ByPhone(ctx context.Context, phone string) (domain.User, bool, error) {
	u, found, err := s.UserStore.ByPhone(ctx, phone)
	if found && s.isDeleted(u.ID) {
		return domain.User{}, false, nil
	}
	return u, found, err
}

// startLoginPasswordServer 起一个服务端，测试（含所有子测试）结束时由 t.Cleanup 关闭。
func startLoginPasswordServer(t *testing.T, opts loginPasswordServerOptions) *loginPasswordServer {
	t.Helper()
	rsaKey := loginPasswordRSAKey(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)

	baseUsers := memory.NewUserStore()
	userStore := &tombstoningUserStore{UserStore: baseUsers, deleted: map[int64]bool{}}
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	updateEventStore := memory.NewUpdateEventStore()
	// 私聊消息（777000 的登录密码改动通知）与 pts 事件、投递任务一起落进这个 store。
	messageStore.AttachUpdateEventStore(updateEventStore)
	// 2FA 云密码的 store 由 auth（登录时判断 password_pending）和 account（设置云密码）共用。
	cloudPasswords := memory.NewPasswordStore()
	activeSessions := NewSessionManager(zaptest.NewLogger(t).Named("sessions"))
	authorizations := memory.NewAuthorizationStore()
	authService := auth.NewService(userStore, authorizations, memory.NewCodeStore(), authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), loginPasswordTestCode,
		auth.WithLoginMessages(messageStore, dialogStore),
		auth.WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messageStore, updateEventStore)),
		auth.WithPasswords(cloudPasswords),
		auth.WithSignUpAllowed(auth.StaticSignUpAllowed(!opts.signUpDisabled)))
	limiter := newCountingRateLimiter()
	loginPasswordStore := memory.NewLoginPasswordStore(baseUsers)
	loginPasswords := loginpasswordapp.NewService(userStore, loginPasswordStore,
		memory.NewLoginPasswordChallengeStore(nil, opts.challengeLimits...), authService,
		loginpasswordapp.WithEnabled(opts.enabled), opts.rateLimits.userRateLimit(limiter),
		loginpasswordapp.WithResetWindow(opts.resetWindow),
		loginpasswordapp.WithChangeNotices(messageStore, zaptest.NewLogger(t).Named("login-password")))
	// 注销账号（account.deleteAccount）走内存的账号生命周期 store，同一事务里删掉登录密码。
	lifecycle := memory.NewAccountLifecycleStore(baseUsers, loginPasswordStore,
		memory.WithAccountLifecyclePasswords(cloudPasswords), memory.WithAccountLifecycleAuthorizations(authorizations))

	deps := rpc.Deps{
		Auth:          authService,
		LoginPassword: loginPasswords,
		Sessions:      activeSessions,
		Account:       account.NewService(cloudPasswords, account.WithUsers(userStore), account.WithAccountLifecycle(lifecycle)),
		Help:          help.NewService(helpStore, helpStore, help.WithPasswordLogin(opts.enabled), help.WithSignUpAllowed(authService.SignUpAllowed)),
		Users:         users.NewService(userStore),
		Updates:       updates.NewService(memory.NewUpdateStateStore(), updateEventStore),
		Contacts:      contacts.NewService(memory.NewContactStore()),
		Dialogs:       dialogs.NewService(dialogStore),
		Messages:      messageapp.NewService(messageStore, dialogStore),
	}
	rpcCfg := rpc.Config{DC: loginPasswordTestDC, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}
	opts.rateLimits.apply(&rpcCfg, &deps, limiter)
	router := rpc.New(rpcCfg, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: loginPasswordTestDC, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router, ActiveSessions: activeSessions})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	// 落库消息（如 777000 的登录密码改动通知）经 outbox 推给在线会话，与生产相同。
	outboxDone := make(chan struct{})
	go func() {
		defer close(outboxDone)
		rpc.NewOutboxDispatcher(updateEventStore, memory.NewDispatchOutboxStore(updateEventStore), activeSessions, zaptest.NewLogger(t).Named("outbox"),
			rpc.WithOutboxInterval(10*time.Millisecond), rpc.WithOutboxUpdateBuilder(router.BuildOutboxUpdates)).Run(ctx)
	}()
	// 子测试是并行的，会在顶层测试函数返回之后才运行；用 Cleanup 而不是 defer，
	// 保证服务端在所有子测试结束后才关闭。
	t.Cleanup(func() {
		cancel()
		if err := <-serveErr; err != nil {
			t.Errorf("serve: %v", err)
		}
		<-outboxDone
	})
	return &loginPasswordServer{rsaKey: rsaKey, addr: tcpAddr, ctx: ctx, users: userStore, loginPasswords: loginPasswordStore, auth: authService}
}

// run 用一个全新的 auth key 连上服务端执行 fn。fn 里的断言失败要 return error，
// 不要 t.Fatal（那只会结束 client 的 goroutine）。
func (s *loginPasswordServer) run(t *testing.T, fn func(ctx context.Context, raw *tg.Client) error) {
	t.Helper()
	if err := s.runWithUpdates(t, nil, fn); err != nil {
		t.Fatal(err)
	}
}

// runWithUpdates 与 run 相同，但把服务端推来的 update 交给 updates（为 nil 时丢弃），
// 并把错误返回给调用方，可以在单独的 goroutine 里保持一个在线会话。
func (s *loginPasswordServer) runWithUpdates(t *testing.T, updates telegram.UpdateHandler, fn func(ctx context.Context, raw *tg.Client) error) error {
	if updates == nil {
		updates = telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil })
	}
	client := telegram.NewClient(1, "hash", telegram.Options{
		PublicKeys:     []exchange.PublicKey{{RSA: &s.rsaKey.PublicKey}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		DCList:         dcs.List{Options: []tg.DCOption{{ID: loginPasswordTestDC, IPAddress: s.addr.IP.String(), Port: s.addr.Port, Static: true}}},
		Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
		SessionStorage: &session.StorageMemory{},
		UpdateHandler:  updates,
	})
	return client.Run(s.ctx, func(ctx context.Context) error {
		return fn(ctx, tg.NewClient(client))
	})
}

func TestLoginPasswordEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true})
	runLoginPasswordManageSubtests(t, srv)

	t.Run("getAppConfig advertises password_login", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			features, ok, err := appConfigTelesrvFeatures(ctx, raw)
			if err != nil {
				return err
			}
			if !ok || len(features) != 1 || features[0] != "password_login" {
				return fmt.Errorf("telesrv_features = %v (present=%v), want [password_login]", features, ok)
			}
			return nil
		})
	})

	t.Run("getLoginPassword on an unregistered phone returns the registration algo", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: "+15550310001"})
			if err != nil {
				return err
			}
			if pw.HasPassword {
				return fmt.Errorf("has_password = true, want false")
			}
			if _, ok := pw.GetCurrentAlgo(); ok {
				return fmt.Errorf("current_algo present on an unregistered phone")
			}
			algo, err := registrationAlgo(pw)
			if err != nil {
				return err
			}
			if len(algo.Salt1) == 0 || len(algo.Salt2) == 0 || algo.G == 0 || len(algo.P) != 256 {
				return fmt.Errorf("new_algo = %+v, want salt1/salt2/g and a 2048-bit p", algo)
			}
			if _, ok := pw.NewSecureAlgo.(*tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000); !ok {
				return fmt.Errorf("new_secure_algo = %T, want pbkdf2", pw.NewSecureAlgo)
			}
			if len(pw.SecureRandom) == 0 {
				return fmt.Errorf("secure_random is empty")
			}
			return nil
		})
	})

	t.Run("signUpWithPassword authorizes at once and sends the welcome message", func(t *testing.T) {
		t.Parallel()
		// 参照：同一服务端上用验证码注册的账号收到的欢迎消息。
		var wantWelcome string
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithCode(ctx, raw, "+15550310010", "Coded"); err != nil {
				return err
			}
			text, err := latestOfficialMessage(ctx, raw)
			wantWelcome = text
			return err
		})
		if wantWelcome == "" {
			t.Fatal("code sign-up received no 777000 welcome message")
		}

		const phone, wantPhone = "+15550310011", "15550310011"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			self, err := signUpWithPassword(ctx, raw, phone, "Pass", "Word", "correct horse")
			if err != nil {
				return err
			}
			if !self.Self || self.Phone != wantPhone || self.FirstName != "Pass" || self.LastName != "Word" {
				return fmt.Errorf("signUp user = %+v, want self phone=%s Pass Word", self, wantPhone)
			}
			full, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err != nil {
				return fmt.Errorf("users.getFullUser after sign-up: %w", err)
			}
			if full.FullUser.ID != self.ID {
				return fmt.Errorf("getFullUser id = %d, want %d", full.FullUser.ID, self.ID)
			}
			welcome, err := latestOfficialMessage(ctx, raw)
			if err != nil {
				return err
			}
			if welcome != wantWelcome {
				return fmt.Errorf("welcome message = %q, want the code sign-up welcome %q", welcome, wantWelcome)
			}
			return nil
		})
	})

	t.Run("signUpWithPassword on an occupied phone returns PHONE_NUMBER_OCCUPIED", func(t *testing.T) {
		t.Parallel()
		// 号码被抢注：后到的一方在号码还没注册时拿到了 new_algo，提交时号码已经被占用。
		const phone = "+15550310020"
		var late tg.AccountPasswordInputSettings
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			var err error
			late, err = newLoginPasswordSettings(ctx, raw, phone, "second password")
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "First", "", "first password")
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{
				PhoneNumber: phone,
				FirstName:   "Second",
				NewSettings: late,
			})
			if err := wantRPCError(err, 400, "PHONE_NUMBER_OCCUPIED"); err != nil {
				return err
			}
			// 失败的一方没有登录：业务方法仍然要求授权。
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
		})
	})

	t.Run("getLoginPassword on a registered phone without a login password returns PASSWORD_LOGIN_NOT_SET", func(t *testing.T) {
		t.Parallel()
		// #31 时这里还断言密码注册的账号也返回 PASSWORD_LOGIN_NOT_SET（过渡行为）；#32 起
		// 密码注册的账号返回 has_password=true（见下一个子测试），只有没设登录密码的账号返回 NOT_SET。
		const codePhone, passwordPhone = "+15550310030", "+15550310031"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithCode(ctx, raw, codePhone, "Coded")
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, passwordPhone, "Pass", "", "a password")
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: codePhone})
			if err := wantRPCError(err, 400, "PASSWORD_LOGIN_NOT_SET"); err != nil {
				return fmt.Errorf("code-registered %s: %w", codePhone, err)
			}
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: passwordPhone})
			if err != nil {
				return fmt.Errorf("password-registered %s: %w", passwordPhone, err)
			}
			if !pw.HasPassword {
				return fmt.Errorf("password-registered %s: has_password = false, want true", passwordPhone)
			}
			return nil
		})
	})

	t.Run("getLoginPassword on a phone with a login password returns a complete SRP challenge", func(t *testing.T) {
		t.Parallel()
		const phone = "+15550310050"
		var registered *tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			settings, err := newLoginPasswordSettings(ctx, raw, phone, "complete")
			if err != nil {
				return err
			}
			registered = settings.NewAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
			_, err = raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{PhoneNumber: phone, FirstName: "Full", NewSettings: settings})
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			first, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			if !first.HasPassword {
				return fmt.Errorf("has_password = false, want true")
			}
			current, ok := first.CurrentAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
			if !ok {
				return fmt.Errorf("current_algo = %T, want SHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow", first.CurrentAlgo)
			}
			// current_algo 是设置密码时客户端提交的 new_algo（salt1 带客户端追加的 32 字节）。
			if !bytes.Equal(current.Salt1, registered.Salt1) || !bytes.Equal(current.Salt2, registered.Salt2) ||
				current.G != registered.G || !bytes.Equal(current.P, registered.P) {
				return fmt.Errorf("current_algo = %+v, want the algo the password was set with %+v", current, registered)
			}
			if len(first.SRPB) != 256 || first.SRPID == 0 {
				return fmt.Errorf("srp_B is %d bytes, srp_id = %d; want 256 bytes and a non-zero id", len(first.SRPB), first.SRPID)
			}
			if _, err := registrationAlgo(first); err != nil {
				return err
			}
			if _, ok := first.NewSecureAlgo.(*tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000); !ok {
				return fmt.Errorf("new_secure_algo = %T, want pbkdf2", first.NewSecureAlgo)
			}
			if len(first.SecureRandom) == 0 {
				return fmt.Errorf("secure_random is empty")
			}
			second, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			if second.SRPID == first.SRPID || bytes.Equal(second.SRPB, first.SRPB) {
				return fmt.Errorf("a second getLoginPassword reused srp_id %d / srp_B; want a fresh challenge", first.SRPID)
			}
			return nil
		})
	})

	t.Run("signInWithPassword with the right password signs in and notifies other sessions", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550310060", "right password"
		// 会话 A：注册后保持在线，等待新设备登录通知。
		notified := make(chan *tg.UpdateServiceNotification, 4)
		handler := telegram.UpdateHandlerFunc(func(_ context.Context, u tg.UpdatesClass) error {
			for _, upd := range updatesFromClass(u) {
				if n, ok := upd.(*tg.UpdateServiceNotification); ok {
					select {
					case notified <- n:
					default:
					}
				}
			}
			return nil
		})
		var self *tg.User
		online := make(chan struct{})
		release := make(chan struct{})
		sessionA := make(chan error, 1)
		go func() {
			sessionA <- srv.runWithUpdates(t, handler, func(ctx context.Context, raw *tg.Client) error {
				var err error
				if self, err = signUpWithPassword(ctx, raw, phone, "Online", "", password); err != nil {
					return err
				}
				// 裸 RPC 置 receivesUpdates，推送才会到达这个会话。
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

		// 会话 B：用密码登录，之后能调用业务方法。
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			res, err := signInWithPassword(ctx, raw, phone, password)
			if err != nil {
				return err
			}
			authz, ok := res.(*tg.AuthAuthorization)
			if !ok {
				return fmt.Errorf("signInWithPassword = %T, want *tg.AuthAuthorization", res)
			}
			if u, ok := authz.User.(*tg.User); !ok || !u.Self || u.ID != self.ID {
				return fmt.Errorf("signInWithPassword user = %+v, want self id %d", authz.User, self.ID)
			}
			full, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err != nil {
				return fmt.Errorf("users.getFullUser after password sign-in: %w", err)
			}
			if full.FullUser.ID != self.ID {
				return fmt.Errorf("getFullUser id = %d, want %d", full.FullUser.ID, self.ID)
			}
			return nil
		})

		select {
		case n := <-notified:
			if !strings.Contains(n.Message, "New login") {
				t.Errorf("service notification = %q, want a new-login notification", n.Message)
			}
		case err := <-sessionA:
			t.Fatalf("session A ended before the new-login notification: %v", err)
		case <-srv.ctx.Done():
			t.Fatal("session A received no new-login notification")
		}
		close(release)
		if err := <-sessionA; err != nil {
			t.Fatalf("session A: %v", err)
		}
	})

	t.Run("signInWithPassword with a wrong password fails and burns the challenge", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550310070", "the real one"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Wrong", "", password)
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			wrong, err := loginPasswordProof(pw, "a guess")
			if err != nil {
				return err
			}
			right, err := loginPasswordProof(pw, password)
			if err != nil {
				return err
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: wrong})
			if err := wantRPCError(err, 400, "PASSWORD_HASH_INVALID"); err != nil {
				return fmt.Errorf("wrong password: %w", err)
			}
			// 失败的尝试也作废了 challenge：同一个 srp_id 即使带正确的证明也不再被接受。
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: right})
			if err := wantRPCError(err, 400, "SRP_ID_INVALID"); err != nil {
				return fmt.Errorf("resubmitting the challenge: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("business call after failed sign-in: %w", err)
			}
			// 成功的证明同样只能用一次：重新取 challenge 登录成功后，重放它被拒绝。
			pw, err = raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			right, err = loginPasswordProof(pw, password)
			if err != nil {
				return err
			}
			if _, err := raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: right}); err != nil {
				return fmt.Errorf("sign-in with a fresh challenge: %w", err)
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: right})
			return wantRPCError(err, 400, "SRP_ID_INVALID")
		})
	})

	t.Run("signInWithPassword rejects a challenge issued to another auth key", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550310080", "bound to a key"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Bound", "", password)
			return err
		})
		// 连接 X 取 challenge 并算出正确的证明；连接 Y（另一把 auth key）拿去提交。
		var stolen tg.InputCheckPasswordSRPClass
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			stolen, err = loginPasswordProof(pw, password)
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: stolen})
			if err := wantRPCError(err, 400, "SRP_ID_INVALID"); err != nil {
				return err
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
		})
	})

	t.Run("signInWithPassword rejects a challenge submitted for a different phone", func(t *testing.T) {
		t.Parallel()
		const phone, other, password = "+15550310085", "+15550310086", "phone bound"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Phone", "", password)
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			proof, err := loginPasswordProof(pw, password)
			if err != nil {
				return err
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: other, Password: proof})
			return wantRPCError(err, 400, "SRP_ID_INVALID")
		})
	})

	t.Run("repeated getLoginPassword from others does not evict a pending challenge", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550310090", "not evicted"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signUpWithPassword(ctx, raw, phone, "Pending", "", password)
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			proof, err := loginPasswordProof(pw, password)
			if err != nil {
				return err
			}
			// 别的连接在这期间反复为同一个号码请求 challenge。
			srvErr := srv.runWithUpdates(t, nil, func(ctx context.Context, other *tg.Client) error {
				for i := 0; i < 3; i++ {
					if _, err := other.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone}); err != nil {
						return err
					}
				}
				return nil
			})
			if srvErr != nil {
				return fmt.Errorf("other connection: %w", srvErr)
			}
			if _, err := raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: proof}); err != nil {
				return fmt.Errorf("sign-in with the original challenge: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})

	t.Run("signInWithPassword on an account with 2FA requires auth.checkPassword", func(t *testing.T) {
		t.Parallel()
		const phone, loginPassword, cloudPassword = "+15550310100", "login password", "cloud password"
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			if _, err := signUpWithPassword(ctx, raw, phone, "Two", "Factor", loginPassword); err != nil {
				return err
			}
			// 已登录会话里开启 2FA 云密码。
			current, err := raw.AccountGetPassword(ctx)
			if err != nil {
				return fmt.Errorf("account.getPassword: %w", err)
			}
			algo, err := registrationAlgo(current)
			if err != nil {
				return err
			}
			settings, err := loginPasswordSettingsFor(algo, cloudPassword)
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
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := signInWithPassword(ctx, raw, phone, loginPassword)
			if err := wantRPCError(err, 401, "SESSION_PASSWORD_NEEDED"); err != nil {
				return fmt.Errorf("signInWithPassword: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return fmt.Errorf("business call while password_pending: %w", err)
			}
			// 登录密码不能代替云密码。
			cloud, err := raw.AccountGetPassword(ctx)
			if err != nil {
				return fmt.Errorf("account.getPassword while password_pending: %w", err)
			}
			wrong, err := loginPasswordProof(cloud, loginPassword)
			if err != nil {
				return err
			}
			if _, err := raw.AuthCheckPassword(ctx, wrong); wantRPCError(err, 400, "PASSWORD_HASH_INVALID") != nil {
				return fmt.Errorf("auth.checkPassword with the login password = %v, want PASSWORD_HASH_INVALID", err)
			}
			cloud, err = raw.AccountGetPassword(ctx)
			if err != nil {
				return err
			}
			proof, err := loginPasswordProof(cloud, cloudPassword)
			if err != nil {
				return err
			}
			if _, err := raw.AuthCheckPassword(ctx, proof); err != nil {
				return fmt.Errorf("auth.checkPassword: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return err
		})
	})

	t.Run("signInWithPassword rejects a deleted account", func(t *testing.T) {
		t.Parallel()
		const phone, password = "+15550310110", "deleted soon"
		var userID int64
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			self, err := signUpWithPassword(ctx, raw, phone, "Gone", "", password)
			if err == nil {
				userID = self.ID
			}
			return err
		})
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			// challenge 是注销之前拿到的，证明也是正确的。
			pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return err
			}
			proof, err := loginPasswordProof(pw, password)
			if err != nil {
				return err
			}
			srv.users.tombstone(userID)
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: proof})
			if err := wantRPCError(err, 406, "PHONE_NUMBER_INVALID"); err != nil {
				return fmt.Errorf("sign-in into a deleted account: %w", err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err := wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED"); err != nil {
				return err
			}
			// 注销后号码不再属于任何账号。
			pw, err = raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
			if err != nil {
				return fmt.Errorf("getLoginPassword after deletion: %w", err)
			}
			if pw.HasPassword {
				return fmt.Errorf("getLoginPassword after deletion: has_password = true")
			}
			return nil
		})
	})

	t.Run("system account phone is refused", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: domain.OfficialSystemPhone})
			if err := wantRPCError(err, 406, "PHONE_NUMBER_INVALID"); err != nil {
				return fmt.Errorf("getLoginPassword(%s): %w", domain.OfficialSystemPhone, err)
			}
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{
				PhoneNumber: domain.OfficialSystemPhone,
				Password:    &tg.InputCheckPasswordSRP{SRPID: 1, A: make([]byte, 256), M1: make([]byte, 32)},
			})
			if err := wantRPCError(err, 400, "SRP_ID_INVALID"); err != nil {
				return fmt.Errorf("signInWithPassword(%s): %w", domain.OfficialSystemPhone, err)
			}
			_, err = raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			return wantRPCError(err, 401, "AUTH_KEY_UNREGISTERED")
		})
	})

	t.Run("signUpWithPassword rejects invalid input without creating an account", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name      string
			phone     string
			firstName string
			mutate    func(*tg.AccountPasswordInputSettings)
			code      int
			errType   string
		}{
			{name: "phone", phone: "+1-not-a-number", firstName: "Bad", code: 406, errType: "PHONE_NUMBER_INVALID"},
			{name: "first name", phone: "+15550310040", firstName: "   ", code: 400, errType: "FIRSTNAME_INVALID"},
			{name: "salt", phone: "+15550310041", firstName: "Salt", code: 400, errType: "NEW_SALT_INVALID",
				mutate: func(s *tg.AccountPasswordInputSettings) {
					// 客户端没有按规则在服务端基础盐后追加 32 字节。
					algo := s.NewAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
					algo.Salt1 = algo.Salt1[:len(algo.Salt1)-32]
				}},
			{name: "email", phone: "+15550310042", firstName: "Mail", code: 400, errType: "NEW_SETTINGS_INVALID",
				mutate: func(s *tg.AccountPasswordInputSettings) { s.SetEmail("someone@example.test") }},
			{name: "missing password hash", phone: "+15550310043", firstName: "Empty", code: 400, errType: "NEW_SETTINGS_INVALID",
				mutate: func(s *tg.AccountPasswordInputSettings) { s.NewPasswordHash = nil }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				srv.run(t, func(ctx context.Context, raw *tg.Client) error {
					settings, err := newLoginPasswordSettings(ctx, raw, "+15550310049", "a password")
					if err != nil {
						return err
					}
					if tc.mutate != nil {
						tc.mutate(&settings)
					}
					_, err = raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{
						PhoneNumber: tc.phone,
						FirstName:   tc.firstName,
						NewSettings: settings,
					})
					if err := wantRPCError(err, tc.code, tc.errType); err != nil {
						return err
					}
					if tc.code == 406 {
						return nil
					}
					// 被拒绝的注册不能留下账号：号码仍然是未注册状态。
					pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: tc.phone})
					if err != nil {
						return fmt.Errorf("getLoginPassword after rejected sign-up: %w", err)
					}
					if pw.HasPassword {
						return fmt.Errorf("has_password after rejected sign-up = true")
					}
					return nil
				})
			})
		}
	})
}

func TestLoginPasswordDisabledEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: false})
	runLoginPasswordManageDisabled(t, srv)

	srv.run(t, func(ctx context.Context, raw *tg.Client) error {
		if features, ok, err := appConfigTelesrvFeatures(ctx, raw); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("telesrv_features = %v with the switch off, want absent", features)
		}
		_, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: "+15550319001"})
		if err := wantRPCError(err, 403, "PASSWORD_LOGIN_DISABLED"); err != nil {
			return fmt.Errorf("getLoginPassword: %w", err)
		}
		// 开关关闭时拿不到 new_algo；请求内容无关紧要，开关检查先于一切参数校验。
		var settings tg.AccountPasswordInputSettings
		settings.SetNewAlgo(&tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
			Salt1: make([]byte, 40), Salt2: make([]byte, 16), G: 3, P: make([]byte, 256),
		})
		settings.SetNewPasswordHash(make([]byte, 256))
		_, err = raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{
			PhoneNumber: "+15550319002",
			FirstName:   "Off",
			NewSettings: settings,
		})
		if err := wantRPCError(err, 403, "PASSWORD_LOGIN_DISABLED"); err != nil {
			return fmt.Errorf("signUpWithPassword: %w", err)
		}
		// 参数本身不合法（未知算法）也不能越过开关检查，得到别的错误码。
		var unknown tg.AccountPasswordInputSettings
		unknown.SetNewAlgo(&tg.PasswordKdfAlgoUnknown{})
		unknown.SetNewPasswordHash([]byte{1})
		_, err = raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{
			PhoneNumber: "+15550319003",
			FirstName:   "Off",
			NewSettings: unknown,
		})
		if err := wantRPCError(err, 403, "PASSWORD_LOGIN_DISABLED"); err != nil {
			return fmt.Errorf("signUpWithPassword with an unknown algo: %w", err)
		}
		// signInWithPassword 同样先检查开关：不论 srp_id 是否存在、证明是否为空。
		for name, check := range map[string]tg.InputCheckPasswordSRPClass{
			"srp proof":   &tg.InputCheckPasswordSRP{SRPID: 1, A: make([]byte, 256), M1: make([]byte, 32)},
			"empty proof": &tg.InputCheckPasswordEmpty{},
		} {
			_, err = raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: "+15550319004", Password: check})
			if err := wantRPCError(err, 403, "PASSWORD_LOGIN_DISABLED"); err != nil {
				return fmt.Errorf("signInWithPassword with %s: %w", name, err)
			}
		}
		return nil
	})
}

// signInWithPassword 走客户端的完整登录时序：getLoginPassword 拿 current_algo、srp_B、
// srp_id，用 td 的 SRP 包算出证明，再 signInWithPassword。
func signInWithPassword(ctx context.Context, raw *tg.Client, phone, password string) (tg.AuthAuthorizationClass, error) {
	pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
	if err != nil {
		return nil, fmt.Errorf("getLoginPassword: %w", err)
	}
	proof, err := loginPasswordProof(pw, password)
	if err != nil {
		return nil, err
	}
	return raw.TelesrvSignInWithPassword(ctx, &tg.TelesrvSignInWithPasswordRequest{PhoneNumber: phone, Password: proof})
}

// loginPasswordProof 按客户端校验密码的方式（td crypto/srp 的 Hash，随机 a）根据
// account.password 里的 current_algo、srp_B、srp_id 算出 inputCheckPasswordSRP。
// 登录密码（getLoginPassword）和 2FA 云密码（account.getPassword）用的是同一套算法。
func loginPasswordProof(pw *tg.AccountPassword, password string) (*tg.InputCheckPasswordSRP, error) {
	if !pw.HasPassword {
		return nil, fmt.Errorf("account.password has_password = false, nothing to prove")
	}
	algo, ok := pw.CurrentAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
	if !ok {
		return nil, fmt.Errorf("current_algo = %T, want SHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow", pw.CurrentAlgo)
	}
	a := make([]byte, 256)
	if _, err := rand.Read(a); err != nil {
		return nil, err
	}
	answer, err := tdsrp.NewSRP(nil).Hash([]byte(password), pw.SRPB, a, tdsrp.Input{
		Salt1: algo.Salt1,
		Salt2: algo.Salt2,
		G:     algo.G,
		P:     algo.P,
	})
	if err != nil {
		return nil, fmt.Errorf("srp hash: %w", err)
	}
	return &tg.InputCheckPasswordSRP{SRPID: pw.SRPID, A: answer.A, M1: answer.M1}, nil
}

// signUpWithPassword 走客户端的完整注册时序：getLoginPassword 拿 new_algo，
// 用 td 的 SRP 包算 verifier，再 signUpWithPassword。返回授权后的自己。
func signUpWithPassword(ctx context.Context, raw *tg.Client, phone, firstName, lastName, password string) (*tg.User, error) {
	settings, err := newLoginPasswordSettings(ctx, raw, phone, password)
	if err != nil {
		return nil, err
	}
	res, err := raw.TelesrvSignUpWithPassword(ctx, &tg.TelesrvSignUpWithPasswordRequest{
		PhoneNumber: phone,
		FirstName:   firstName,
		LastName:    lastName,
		NewSettings: settings,
	})
	if err != nil {
		return nil, err
	}
	authz, ok := res.(*tg.AuthAuthorization)
	if !ok {
		return nil, fmt.Errorf("signUpWithPassword result = %T, want *tg.AuthAuthorization", res)
	}
	self, ok := authz.User.(*tg.User)
	if !ok {
		return nil, fmt.Errorf("signUpWithPassword user = %T, want *tg.User", authz.User)
	}
	return self, nil
}

// newLoginPasswordSettings 用 phone 查询注册参数并算出 new_settings。
func newLoginPasswordSettings(ctx context.Context, raw *tg.Client, phone, password string) (tg.AccountPasswordInputSettings, error) {
	pw, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
	if err != nil {
		return tg.AccountPasswordInputSettings{}, fmt.Errorf("getLoginPassword: %w", err)
	}
	algo, err := registrationAlgo(pw)
	if err != nil {
		return tg.AccountPasswordInputSettings{}, err
	}
	return loginPasswordSettingsFor(algo, password)
}

// loginPasswordSettingsFor 按客户端设置新密码的方式（td crypto/srp 的 NewHash：salt1 追加
// 32 字节随机数，算出 v）构造 new_settings。
func loginPasswordSettingsFor(algo *tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow, password string) (tg.AccountPasswordInputSettings, error) {
	hash, newSalt1, err := tdsrp.NewSRP(rand.Reader).NewHash([]byte(password), tdsrp.Input{
		Salt1: algo.Salt1,
		Salt2: algo.Salt2,
		G:     algo.G,
		P:     algo.P,
	})
	if err != nil {
		return tg.AccountPasswordInputSettings{}, fmt.Errorf("srp new hash: %w", err)
	}
	var settings tg.AccountPasswordInputSettings
	settings.SetNewAlgo(&tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
		Salt1: newSalt1,
		Salt2: append([]byte(nil), algo.Salt2...),
		G:     algo.G,
		P:     append([]byte(nil), algo.P...),
	})
	settings.SetNewPasswordHash(hash)
	settings.SetHint("ignored by the server")
	return settings, nil
}

func registrationAlgo(pw *tg.AccountPassword) (*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow, error) {
	algo, ok := pw.NewAlgo.(*tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)
	if !ok {
		return nil, fmt.Errorf("new_algo = %T, want SHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow", pw.NewAlgo)
	}
	return algo, nil
}

// signUpWithCode 走现有的验证码注册（开发固定码）。
func signUpWithCode(ctx context.Context, raw *tg.Client, phone, firstName string) (*tg.User, error) {
	sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
	if err != nil {
		return nil, err
	}
	hash := sent.(*tg.AuthSentCode).PhoneCodeHash
	if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: hash, PhoneCode: loginPasswordTestCode}); err != nil {
		return nil, err
	}
	res, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: hash, FirstName: firstName})
	if err != nil {
		return nil, err
	}
	return res.(*tg.AuthAuthorization).User.(*tg.User), nil
}

// latestOfficialMessage 返回系统账号 777000 对话里最新一条消息的正文。
func latestOfficialMessage(ctx context.Context, raw *tg.Client) (string, error) {
	res, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
	if err != nil {
		return "", fmt.Errorf("messages.getDialogs: %w", err)
	}
	var (
		dialogList  []tg.DialogClass
		messageList []tg.MessageClass
	)
	switch v := res.(type) {
	case *tg.MessagesDialogs:
		dialogList, messageList = v.Dialogs, v.Messages
	case *tg.MessagesDialogsSlice:
		dialogList, messageList = v.Dialogs, v.Messages
	default:
		return "", fmt.Errorf("messages.getDialogs = %T, want dialogs", res)
	}
	for _, d := range dialogList {
		dialog, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		peer, ok := dialog.Peer.(*tg.PeerUser)
		if !ok || peer.UserID != domain.OfficialSystemUserID {
			continue
		}
		for _, m := range messageList {
			if msg, ok := m.(*tg.Message); ok && msg.ID == dialog.TopMessage {
				return msg.Message, nil
			}
		}
		return "", fmt.Errorf("777000 top message %d missing from getDialogs messages", dialog.TopMessage)
	}
	return "", fmt.Errorf("no 777000 dialog in getDialogs (%d dialogs)", len(dialogList))
}

// appConfigTelesrvFeatures 读取 help.getAppConfig 里的 telesrv_features。
func appConfigTelesrvFeatures(ctx context.Context, raw *tg.Client) ([]string, bool, error) {
	res, err := raw.HelpGetAppConfig(ctx, 0)
	if err != nil {
		return nil, false, fmt.Errorf("help.getAppConfig: %w", err)
	}
	cfg, ok := res.(*tg.HelpAppConfig)
	if !ok {
		return nil, false, fmt.Errorf("help.getAppConfig = %T, want *tg.HelpAppConfig", res)
	}
	object, ok := cfg.Config.(*tg.JSONObject)
	if !ok {
		return nil, false, fmt.Errorf("app config = %T, want *tg.JSONObject", cfg.Config)
	}
	for _, item := range object.Value {
		if item.Key != "telesrv_features" {
			continue
		}
		array, ok := item.Value.(*tg.JSONArray)
		if !ok {
			return nil, true, fmt.Errorf("telesrv_features = %T, want array", item.Value)
		}
		var out []string
		for _, v := range array.Value {
			s, ok := v.(*tg.JSONString)
			if !ok {
				return nil, true, fmt.Errorf("telesrv_features item = %T, want string", v)
			}
			out = append(out, s.Value)
		}
		return out, true, nil
	}
	return nil, false, nil
}

// wantRPCError 断言 err 是指定 code 和 type 的 rpc_error。
func wantRPCError(err error, code int, errType string) error {
	if err == nil {
		return fmt.Errorf("succeeded, want %d %s", code, errType)
	}
	var rpcErr *tgerr.Error
	if !errors.As(err, &rpcErr) {
		return fmt.Errorf("err = %v, want rpc error %d %s", err, code, errType)
	}
	if rpcErr.Type != errType || rpcErr.Code != code {
		return fmt.Errorf("rpc error = %d %s, want %d %s", rpcErr.Code, rpcErr.Type, code, errType)
	}
	return nil
}
