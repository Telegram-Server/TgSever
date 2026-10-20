package mtprotoedge

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// 关闭验证码登录（#90）的端到端测试：只证明各层接通了——真实 td 客户端经 TCP、握手、
// edge、rpc、业务层。开或关 × 每个验证码入口的功能矩阵在
// internal/app/auth/code_login_switch_test.go 里，不在这一层铺开。
//
// 防锁死要求密码登录总开关打开，这里起一个密码登录开启、验证码登录关闭的服务端。
func TestCodeLoginDisabledEndToEnd(t *testing.T) {
	t.Parallel()
	srv := startLoginPasswordServer(t, loginPasswordServerOptions{enabled: true, codeLoginDisabled: true})

	t.Run("getAppConfig before login carries telesrv_code_login_disabled", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			value, ok, err := appConfigValue(ctx, raw, "telesrv_code_login_disabled")
			if err != nil {
				return err
			}
			if b, isBool := value.(*tg.JSONBool); !ok || !isBool || !b.Value {
				return fmt.Errorf("telesrv_code_login_disabled = %#v (present=%v), want true", value, ok)
			}
			return nil
		})
	})

	t.Run("sendCode returns CODE_LOGIN_DISABLED", func(t *testing.T) {
		t.Parallel()
		srv.run(t, func(ctx context.Context, raw *tg.Client) error {
			_, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+15550900001", APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
			if err := wantRPCError(err, 400, "CODE_LOGIN_DISABLED"); err != nil {
				return fmt.Errorf("auth.sendCode: %w", err)
			}
			return nil
		})
	})
}
