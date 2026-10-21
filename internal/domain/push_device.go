package domain

import "time"

// 推送 token 类型（account.registerDevice 的 token_type）。本期只推 APNs 普通推送与
// VoIP 推送，其他类型（如 FCM）照样登记保存，但不推送。
const (
	PushTokenAPNs = 1
	PushTokenFCM  = 2
	PushTokenVoIP = 9
)

// PushDevice 是一条设备推送登记：某个授权（perm auth key）在某种 token 类型下登记的
// 推送 token。唯一性按 (AuthKeyID, TokenType)，同一授权重复登记覆盖旧值；同一个 token
// 可以挂在多个授权下（同一台 iPhone 登录多个账号）。授权被删除时登记随之消失。
type PushDevice struct {
	// AuthKeyID 是登记所属的授权（authorizations 主键，即 perm auth key）。
	AuthKeyID [8]byte
	// UserID 是登记时授权所属的用户。授权的 user 可能被原地改写（扫码登录），
	// 发送前要和授权当前的 user 核对。
	UserID    int64
	TokenType int
	Token     string
	// AppSandbox 为真时走 APNs 开发网关。
	AppSandbox bool
	// Secret 是客户端提交的 payload 加密密钥；为空表示不加密。
	Secret []byte
	// NoMuted 原样保存客户端的 no_muted 标志（本期不区分）。
	NoMuted bool
	// OtherUIDs 是同一台设备上登录的其他账号，这些账号发来的消息不推送。
	OtherUIDs []int64
	// LangCode 是登记时客户端的语言代码，服务端按它渲染推送文案；为空时由调用方回落到 en。
	LangCode string
	// CreatedAt 是（最近一次）登记时间，由存储维护，写入时忽略调用方传入的值。
	CreatedAt time.Time
}
