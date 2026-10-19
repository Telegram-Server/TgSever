package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net"
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
// 每个场景是一个并行子测试、用自己的手机号隔离。后续票（#32 登录、#33 限流、
// #34 修改密码、#35 忘记密码）往这里加子测试即可；需要调小阈值或窗口时，
// 在 loginPasswordServerOptions 里加字段，由 startLoginPasswordServer 传给业务层。
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
}

// loginPasswordServer 是一个跑在随机端口上的完整服务端，供多个子测试并发使用。
type loginPasswordServer struct {
	rsaKey *rsa.PrivateKey
	addr   *net.TCPAddr
	ctx    context.Context
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

	userStore := memory.NewUserStore()
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	updateEventStore := memory.NewUpdateEventStore()
	authService := auth.NewService(userStore, memory.NewAuthorizationStore(), memory.NewCodeStore(), authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), loginPasswordTestCode,
		auth.WithLoginMessages(messageStore, dialogStore),
		auth.WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messageStore, updateEventStore)))
	loginPasswords := loginpasswordapp.NewService(userStore, memory.NewLoginPasswordStore(userStore), authService,
		loginpasswordapp.WithEnabled(opts.enabled))

	deps := rpc.Deps{
		Auth:          authService,
		LoginPassword: loginPasswords,
		Account:       account.NewService(memory.NewPasswordStore(), account.WithUsers(userStore)),
		Help:          help.NewService(helpStore, helpStore, help.WithPasswordLogin(opts.enabled)),
		Users:         users.NewService(userStore),
		Updates:       updates.NewService(memory.NewUpdateStateStore(), updateEventStore),
		Contacts:      contacts.NewService(memory.NewContactStore()),
		Dialogs:       dialogs.NewService(dialogStore),
		Messages:      messageapp.NewService(messageStore, dialogStore),
	}
	router := rpc.New(rpc.Config{DC: loginPasswordTestDC, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: loginPasswordTestDC, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	// 子测试是并行的，会在顶层测试函数返回之后才运行；用 Cleanup 而不是 defer，
	// 保证服务端在所有子测试结束后才关闭。
	t.Cleanup(func() {
		cancel()
		if err := <-serveErr; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return &loginPasswordServer{rsaKey: rsaKey, addr: tcpAddr, ctx: ctx}
}

// run 用一个全新的 auth key 连上服务端执行 fn。fn 里的断言失败要 return error，
// 不要 t.Fatal（那只会结束 client 的 goroutine）。
func (s *loginPasswordServer) run(t *testing.T, fn func(ctx context.Context, raw *tg.Client) error) {
	t.Helper()
	client := telegram.NewClient(1, "hash", telegram.Options{
		PublicKeys:     []exchange.PublicKey{{RSA: &s.rsaKey.PublicKey}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		DCList:         dcs.List{Options: []tg.DCOption{{ID: loginPasswordTestDC, IPAddress: s.addr.IP.String(), Port: s.addr.Port, Static: true}}},
		Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
		SessionStorage: &session.StorageMemory{},
		UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
	})
	if err := client.Run(s.ctx, func(ctx context.Context) error {
		return fn(ctx, tg.NewClient(client))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginPasswordEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true})

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

	t.Run("getLoginPassword on a registered phone returns PASSWORD_LOGIN_NOT_SET", func(t *testing.T) {
		t.Parallel()
		// 过渡行为（#31）：已有账号一律返回 PASSWORD_LOGIN_NOT_SET；
		// 已设登录密码的分支由 #32 补上，届时密码注册的账号会返回 has_password=true。
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
			for _, phone := range []string{codePhone, passwordPhone} {
				_, err := raw.TelesrvGetLoginPassword(ctx, &tg.TelesrvGetLoginPasswordRequest{PhoneNumber: phone})
				if err := wantRPCError(err, 400, "PASSWORD_LOGIN_NOT_SET"); err != nil {
					return fmt.Errorf("%s: %w", phone, err)
				}
			}
			return nil
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
		return nil
	})
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
