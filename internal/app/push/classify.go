package push

import "net/http"

// Outcome 是一次推送结果的分类，决定推送服务的后续处理。
type Outcome uint8

const (
	// OutcomeSent 是网关已接受。
	OutcomeSent Outcome = iota
	// OutcomeInvalidToken 是 token 已失效：删除对应登记，之后不再推送。
	OutcomeInvalidToken
	// OutcomeRetryable 是暂时性失败（限流、网关 5xx、超时或网络错误）：有限次退避重试。
	OutcomeRetryable
	// OutcomePermanent 是其他失败（证书、topic、payload 等配置问题）：只记日志，不重试。
	OutcomePermanent
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSent:
		return "sent"
	case OutcomeInvalidToken:
		return "invalid_token"
	case OutcomeRetryable:
		return "retryable"
	default:
		return "permanent"
	}
}

// APNs 表示 token 失效的 reason（https://developer.apple.com/documentation/usernotifications/handling-notification-responses-from-apns）。
const (
	reasonBadDeviceToken = "BadDeviceToken"
	reasonUnregistered   = "Unregistered"
)

// APNs 表示服务端配置错误的 reason：token 与 topic 不匹配、topic 不允许推送。
// 这是 TELESRV_APNS_TOPIC / 证书配错（或开发/生产网关混用），token 本身没失效，
// 不能删登记——否则一次配错就清空所有设备的登记。按永久失败处理并大声告警。
const (
	reasonDeviceTokenNotForTopic = "DeviceTokenNotForTopic"
	reasonTopicDisallowed        = "TopicDisallowed"
)

// IsMisconfiguration 报告网关响应是否表示服务端推送配置错误（见 reasonDeviceTokenNotForTopic）。
func IsMisconfiguration(result Result) bool {
	return result.Reason == reasonDeviceTokenNotForTopic || result.Reason == reasonTopicDisallowed
}

// Classify 把 Sender.Send 的返回值映射成结果分类。err 非 nil 表示没有得到网关响应
// （超时、网络错误），按可重试处理。
func Classify(result Result, err error) Outcome {
	if err != nil {
		return OutcomeRetryable
	}
	if IsMisconfiguration(result) {
		return OutcomePermanent
	}
	switch result.Reason {
	case reasonBadDeviceToken, reasonUnregistered:
		return OutcomeInvalidToken
	}
	switch {
	case result.StatusCode == http.StatusOK:
		return OutcomeSent
	case result.StatusCode == http.StatusGone:
		// 410：token 已不再对该 topic 有效（Unregistered、ExpiredToken）。
		return OutcomeInvalidToken
	case result.StatusCode == http.StatusTooManyRequests, result.StatusCode >= 500 && result.StatusCode <= 599:
		return OutcomeRetryable
	default:
		return OutcomePermanent
	}
}
