package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// 关闭注册（#74/#75）的端到端测试：只证明各层接通了——真实 td 客户端经 TCP、握手、
// edge、rpc、业务层。开或关 × 新号码或已有号码 × 每个入口的功能矩阵在
// internal/app/auth/signup_switch_test.go 里，不在这一层铺开。
//
// 注册开关是服务端级别的配置，这里单独起一个关闭注册的服务端（复用密码登录测试的
// startLoginPasswordServer，密码登录保持关闭）。后台建号（#78）的断言——"后台建号命令
// 创建的账号可以用验证码登录"——往 TestSignUpDisabledEndToEnd 里加子测试即可。
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
