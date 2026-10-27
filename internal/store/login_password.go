package store

import (
	"context"
	"errors"
	"time"

	"telesrv/internal/domain"
)

var (
	// ErrLoginPasswordInvalid 表示要写入的登录密码缺少 salt1、salt2 或 verifier。
	ErrLoginPasswordInvalid = errors.New("login password material missing")
	// ErrLoginPasswordChallengeExists 表示 srp_id 已被另一个未取走的 challenge 占用。
	ErrLoginPasswordChallengeExists = errors.New("login password challenge id already in use")
)

// LoginPasswordChallenge 是 telesrv.getLoginPassword 发出的一次性 SRP challenge
// （密码登录用）。它只存在进程内存里，不写进用户记录：别人反复请求只会产生新的
// challenge，挤不掉正常用户手里那个。
type LoginPasswordChallenge struct {
	// SRPID 是下发给客户端的 srp_id，也是存储的 key（非零）。
	SRPID int64
	// UserID 和 Phone（规范化后）是发出 challenge 时号码对应的账号。
	UserID int64
	Phone  string
	// AuthKeyID 是发起请求的原始 auth_key（RawAuthKeyIDFrom），只有它能使用这个 challenge。
	AuthKeyID [8]byte
	// Secret 是服务端私密值 b，B = k*v + g^b 是下发的 srp_B。
	Secret []byte
	B      []byte
	// Verifier、Salt1、Salt2 是发出 challenge 时存储里的登录密码；登录时与重新读出的
	// 密码比较，不一致说明期间密码被改过。
	Verifier []byte
	Salt1    []byte
	Salt2    []byte
	// ExpiresAt 由调用方按自己的时钟算好（发出时刻 + TTL），过期判断也由调用方做。
	ExpiresAt time.Time
}

// LoginPasswordChallengeStore 暂存密码登录的 SRP challenge（进程内、短 TTL、用后即焚）。
type LoginPasswordChallengeStore interface {
	// SaveLoginPasswordChallenge 保存 challenge。SRPID 已被占用时返回
	// ErrLoginPasswordChallengeExists，绝不覆盖别人的 challenge。
	SaveLoginPasswordChallenge(ctx context.Context, c LoginPasswordChallenge) error
	// TakeLoginPasswordChallenge 原子地取出并删除 challenge；不存在返回 found=false。
	// 不判断是否过期（ExpiresAt 原样返回，由调用方按自己的时钟判断），但过期的记录
	// 也会被删除，同一个 SRPID 第二次取一定是 found=false。
	TakeLoginPasswordChallenge(ctx context.Context, srpID int64) (c LoginPasswordChallenge, found bool, err error)
}

// LoginPasswordStore 持久化账号的登录密码（手机号 + 密码登录），与 2FA 的 PasswordStore
// 互相独立。CreatedAt/UpdatedAt 由存储维护，写入时忽略调用方传入的值。
type LoginPasswordStore interface {
	// GetLoginPassword 读取账号的登录密码；没有设置时 found=false。
	GetLoginPassword(ctx context.Context, userID int64) (domain.LoginPassword, bool, error)
	// SetLoginPassword 设置或替换账号的登录密码。账号不存在返回 domain.ErrUserNotFound，
	// 密码材料不完整返回 ErrLoginPasswordInvalid。
	SetLoginPassword(ctx context.Context, userID int64, password domain.LoginPassword) error
	// DeleteLoginPassword 删除账号的登录密码，deleted 表示删除前确实存在。
	DeleteLoginPassword(ctx context.Context, userID int64) (deleted bool, err error)
}

// 建号时一起写入登录密码走 AccountCreationStore.CreateAccount（AccountExtras.LoginPassword），
// 建号和写密码在同一个事务里。
