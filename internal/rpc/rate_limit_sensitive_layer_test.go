package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
)

// 生产走按 Layer 准入的 DispatchAdmitted；敏感方法组的限流必须在这条路径上生效，
// 而不只是旧的 Router.Dispatch 测试接缝。
func TestSensitiveRPCRateLimitAppliesOnLayerDispatch(t *testing.T) {
	const userID = int64(1001)
	for _, profile := range []tlprofile.Profile{
		tlprofile.Profile225,
		tlprofile.Profile228,
	} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			limiter := &captureRateLimiter{block: true, retryAfter: 42}
			router := New(
				Config{DC: 2, IP: "127.0.0.1", Port: 2398, SRPRateLimit: 1},
				Deps{Limiter: limiter},
				zaptest.NewLogger(t),
				clock.System,
			)
			body := encodeExactLayerRPC(t, profile, &tg.AccountGetPasswordRequest{})
			admitted, err := router.AdmitLayer(profile, &body, tlprofile.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			_, method, err := router.DispatchAdmitted(WithUserID(context.Background(), userID), [8]byte{1}, 10, 0, 1, admitted)
			if wait, ok := tgerr.AsFloodWait(err); method != "account.getPassword" || !ok || wait != 42*time.Second {
				t.Fatalf("DispatchAdmitted = method:%q err:%v, want FLOOD_WAIT_42", method, err)
			}
			if len(limiter.calls) != 1 || limiter.calls[0].key != srpRateLimitKeyPrefix+"u:1001" {
				t.Fatalf("limiter calls = %+v, want one call on the user's SRP budget", limiter.calls)
			}
		})
	}
}
