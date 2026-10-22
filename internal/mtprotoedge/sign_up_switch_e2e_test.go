package mtprotoedge

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// 关闭注册（#74/#75）的端到端测试：只证明各层接通了——真实 td 客户端经 TCP、握手、
// edge、rpc、业务层。开或关 × 新号码或已有号码 × 每个入口的功能矩阵在
// internal/app/auth/signup_switch_test.go 里，不在这一层铺开。
//
// 注册开关是服务端级别的配置，这里单独起一个关闭注册的服务端（复用密码登录测试的
// startLoginPasswordServer，密码登录保持关闭）。后台建号（#78）的断言：后台建号命令
// 创建的账号可以用验证码登录。
func TestSignUpDisabledEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{signUpDisabled: true})

	t.Run("getAppConfig before login carries telesrv_sign_up_disabled", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			value, ok, err := appConfigValue(ctx, raw, "telesrv_sign_up_disabled")
			if err != nil {
				return err
			}
			if b, isBool := value.(*tg.JSONBool); !ok || !isBool || !b.Value {
				return fmt.Errorf("telesrv_sign_up_disabled = %#v (present=%v), want true", value, ok)
			}
			return nil
		})
	})

	t.Run("admin-created account can sign in with a login code", func(t *testing.T) {
		t.Parallel()
		const phone = "15550750002"
		// 后台建号命令在进程内接上服务端的 auth 服务（生产里经 admin API 调同一个方法）。
		adminSvc := admin.NewService(admin.Dependencies{Commands: &e2eAdminCommands{}, AccountCreator: srv.auth})
		created, err := adminSvc.CreateAccount(context.Background(), admin.CreateAccountRequest{
			CommandMeta: admin.CommandMeta{CommandID: "e2e-create-account", Actor: "ops", Reason: "new member"},
			Phone:       "+" + phone, FirstName: "Invited",
		})
		if err != nil {
			t.Fatalf("admin CreateAccount: result=%+v err=%v", created, err)
		}
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+" + phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
			if err != nil {
				return fmt.Errorf("auth.sendCode for admin-created account: %w", err)
			}
			res, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "+" + phone, PhoneCodeHash: sent.(*tg.AuthSentCode).PhoneCodeHash, PhoneCode: loginPasswordTestCode})
			if err != nil {
				return fmt.Errorf("auth.signIn for admin-created account: %w", err)
			}
			authz, ok := res.(*tg.AuthAuthorization)
			if !ok {
				return fmt.Errorf("auth.signIn = %T, want *tg.AuthAuthorization", res)
			}
			if user, _ := authz.User.(*tg.User); user == nil || user.ID != created.Details["user_id"] {
				return fmt.Errorf("signed in as %#v, want admin-created user %v", authz.User, created.Details["user_id"])
			}
			return nil
		})
	})

	t.Run("sendCode for a new number returns SIGN_UP_DISABLED", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+15550750001", APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
			if err := wantRPCError(err, 400, "SIGN_UP_DISABLED"); err != nil {
				return fmt.Errorf("auth.sendCode: %w", err)
			}
			return nil
		})
	})
}

// appConfigValue 读取 help.getAppConfig 顶层的某个 key。
func appConfigValue(ctx context.Context, raw *tg.Client, key string) (tg.JSONValueClass, bool, error) {
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
		if item.Key == key {
			return item.Value, true, nil
		}
	}
	return nil, false, nil
}

// e2eAdminCommands 是后台命令日志的最小内存实现，只够跑一条建号命令；
// 命令日志与审计的行为在 internal/admin 和 postgres 的测试里覆盖。
type e2eAdminCommands struct {
	mu    sync.Mutex
	items map[string]domain.AdminCommand
}

func (r *e2eAdminCommands) BeginCommand(_ context.Context, cmd domain.AdminCommand) (domain.AdminCommand, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = map[string]domain.AdminCommand{}
	}
	if existing, ok := r.items[cmd.CommandID]; ok {
		return existing, false, nil
	}
	r.items[cmd.CommandID] = cmd
	return cmd, true, nil
}

func (r *e2eAdminCommands) Command(_ context.Context, commandID string) (domain.AdminCommand, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd, ok := r.items[commandID]
	return cmd, ok, nil
}

func (r *e2eAdminCommands) FinishCommand(_ context.Context, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmd := r.items[commandID]
	cmd.Status, cmd.ResultJSON, cmd.Error = status, resultJSON, errorText
	r.items[commandID] = cmd
	return cmd, nil
}

func (r *e2eAdminCommands) ListRecentCommands(context.Context, int, string) ([]domain.AdminCommand, error) {
	return nil, nil
}
