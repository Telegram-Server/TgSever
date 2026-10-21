package store

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

// ErrPushDeviceAuthorizationNotFound 表示登记所属的授权不存在（未登录或已被删除）。
var ErrPushDeviceAuthorizationNotFound = errors.New("push device authorization not found")

// PushDeviceStore 持久化设备推送登记（account.registerDevice）。登记绑定到授权上：
// 删除授权的所有路径（退出登录、终止会话、注销账号、管理员吊销、bot 重置、删除 auth key）
// 都会带走该授权下的登记。
type PushDeviceStore interface {
	// RegisterPushDevice 写入或覆盖 (AuthKeyID, TokenType) 对应的登记，CreatedAt 取当前时间。
	// 授权不存在返回 ErrPushDeviceAuthorizationNotFound。
	RegisterPushDevice(ctx context.Context, device domain.PushDevice) error
	// ListPushDevicesByUser 返回登记时属于 userID 的全部登记（顺序不保证）。
	ListPushDevicesByUser(ctx context.Context, userID int64) ([]domain.PushDevice, error)
	// ListPushDevicesByUsers 一次返回多个用户的全部登记（群组/频道推送按批读取，顺序不保证）。
	ListPushDevicesByUsers(ctx context.Context, userIDs []int64) ([]domain.PushDevice, error)
	// DeletePushDevice 删除授权下指定 token 类型的登记，deleted 表示删除前确实存在。
	DeletePushDevice(ctx context.Context, authKeyID [8]byte, tokenType int) (deleted bool, err error)
	// DeleteStalePushDevice 删除推送服务判定失效的登记（token 失效、user 与授权不一致）：
	// 只有 (AuthKeyID, TokenType) 下的登记仍是 device 读到时的 Token 与 UserID 才删除，
	// 读到之后客户端重新登记的新登记保留。deleted 表示确实删除了。
	DeleteStalePushDevice(ctx context.Context, device domain.PushDevice) (deleted bool, err error)
}
