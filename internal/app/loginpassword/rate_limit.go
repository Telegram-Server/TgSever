package loginpassword

import (
	"context"
	"strconv"
	"time"

	"telesrv/internal/domain"
)

// userRateLimitKeyPrefix 是按目标用户限流的 key 前缀，后接用户 ID。
const userRateLimitKeyPrefix = "auth:login-password:user:"

// RateLimiter 是按目标用户限流用的窗口限流器，与 store.RateLimiter 的 AllowN 相同。
type RateLimiter interface {
	AllowN(ctx context.Context, key string, cost, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

// userRateLimit 是 signInWithPassword 按目标用户的限流参数。
type userRateLimit struct {
	limiter RateLimiter
	limit   int
	window  time.Duration
}

// WithUserRateLimit 设置 signInWithPassword 按目标用户的限流
// （TELESRV_PASSWORD_LOGIN_USER_RATE_LIMIT / _WINDOW）：同一个用户在 window 内最多被尝试
// limit 次，不论证明对错、来自哪个 auth_key。limiter 为 nil、limit<=0 或 window<=0 时不限流。
//
// 这意味着攻击者可以暂时锁住某个用户的密码登录；这是有意的取舍，被锁的用户仍然可以用验证码登录，
// 而不按用户限流的话，换 auth_key 就能无限次猜密码。
func WithUserRateLimit(limiter RateLimiter, limit int, window time.Duration) Option {
	return func(s *Service) {
		s.userLimit = userRateLimit{limiter: limiter, limit: limit, window: window}
	}
}

// checkUserRateLimit 为针对 userID 的一次密码登录尝试计数，超额时返回
// domain.LoginPasswordFloodWaitError。
func (s *Service) checkUserRateLimit(ctx context.Context, userID int64) error {
	l := s.userLimit
	if l.limiter == nil || l.limit <= 0 || l.window <= 0 {
		return nil
	}
	allowed, retryAfter, err := l.limiter.AllowN(ctx, userRateLimitKeyPrefix+strconv.FormatInt(userID, 10), 1, l.limit, l.window)
	if err != nil {
		return err
	}
	if !allowed {
		return domain.NewLoginPasswordFloodWaitError(retryAfter)
	}
	return nil
}
