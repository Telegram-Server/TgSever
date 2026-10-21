package push

import (
	"context"
	"time"
)

// PushType 是 APNs 推送类型，决定 apns-push-type 与 topic（VoIP 用 "bundle id + .voip"）。
type PushType uint8

const (
	// PushTypeAlert 是普通推送（新消息、已读/删除同步），topic 为 bundle id。
	PushTypeAlert PushType = iota
	// PushTypeVoIP 是 PushKit VoIP 推送（来电，#99），topic 为 bundle id + ".voip"。
	PushTypeVoIP
)

func (t PushType) String() string {
	if t == PushTypeVoIP {
		return "voip"
	}
	return "alert"
}

// APNs 优先级：新消息 10（立即），已读/删除同步 5（省电，#100）。
const (
	PriorityHigh = 10
	PriorityLow  = 5
)

// Notification 是一条要发给某台设备的推送请求，payload 已经生成（必要时已加密）。
type Notification struct {
	// Token 是设备登记的 token（APNs 是小写 hex 字符串），原样交给网关。
	Token string
	// Sandbox 为真时走 APNs 开发网关。
	Sandbox  bool
	Type     PushType
	Priority int
	// CollapseID 非空时设置 apns-collapse-id（已读/删除同步用，#100）。
	CollapseID string
	// Expiration 是 apns-expiration；零值表示交给网关默认。
	Expiration time.Time
	// ExpireImmediately 为真时发送 apns-expiration: 0（优先于 Expiration）：网关只尝试投递一次、
	// 不存储补发。VoIP 来电用它（#99）。
	ExpireImmediately bool
	Payload           []byte
}

// Result 是网关对一次推送的原始响应。结果分类（成功 / token 失效 / 可重试 / 永久失败）
// 在 #101 里基于 StatusCode 与 Reason 做成纯函数。
type Result struct {
	StatusCode int
	// Reason 是 APNs 返回的失败原因（BadDeviceToken、Unregistered…），成功时为空。
	Reason string
	// ID 是网关分配的 apns-id，仅用于日志关联。
	ID string
}

// Sent 报告网关是否接受了这条推送。
func (r Result) Sent() bool { return r.StatusCode == 200 }

// Sender 把一条推送请求发到推送网关。APNs 实现在 push/apns；以后加 FCM 只需再写一个实现。
// err 只表示请求没有得到网关响应（网络错误、超时）；网关拒绝时 err 为 nil，原因在 Result 里。
type Sender interface {
	Send(ctx context.Context, n Notification) (Result, error)
}
