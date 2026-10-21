package push

import (
	"net/http"
	"strings"
)

// MetricsSink 是按字符串标签计数的指标后端（internal/observability/metrics.Registry 满足它）。
type MetricsSink interface {
	PushSendResult(kind, result string)
	PushQueueDropped(kind string)
	PushDeviceRemoved(reason string)
}

// SinkMetrics 把推送服务的 Metrics 回调接到 MetricsSink：发送结果先归成 SendResultLabel 的分类，
// 避免 APNs 的 reason 原样变成标签。
func SinkMetrics(sink MetricsSink) Metrics {
	if sink == nil {
		return nopMetrics{}
	}
	return sinkMetrics{sink: sink}
}

type sinkMetrics struct{ sink MetricsSink }

func (m sinkMetrics) PushQueueDropped(kind string)    { m.sink.PushQueueDropped(kind) }
func (m sinkMetrics) PushDeviceRemoved(reason string) { m.sink.PushDeviceRemoved(reason) }
func (m sinkMetrics) PushSent(kind string, result Result, err error) {
	m.sink.PushSendResult(kind, SendResultLabel(result, err))
}

// 发送结果分类（指标标签）。
const (
	ResultLabelSent         = "sent"
	ResultLabelInvalidToken = "invalid_token"
	ResultLabelRetryable    = "retryable"
	ResultLabelFailed       = "failed"
)

// SendResultLabel 把一次发送归成四类：成功、token 失效（410、BadDeviceToken、Unregistered、
// DeviceTokenNotForTopic）、可重试（没有响应——网络错误或超时，429、5xx）、其余为永久失败。
func SendResultLabel(result Result, err error) string {
	if err != nil {
		return ResultLabelRetryable
	}
	switch {
	case result.Sent():
		return ResultLabelSent
	case result.StatusCode == http.StatusGone:
		return ResultLabelInvalidToken
	}
	switch strings.TrimSpace(result.Reason) {
	case "BadDeviceToken", "Unregistered", "DeviceTokenNotForTopic":
		return ResultLabelInvalidToken
	}
	if result.StatusCode == http.StatusTooManyRequests || result.StatusCode >= 500 {
		return ResultLabelRetryable
	}
	return ResultLabelFailed
}

// QueueLength 返回当前排队等待处理的任务数（监控指标用）。
func (s *Service) QueueLength() int {
	if s == nil {
		return 0
	}
	return len(s.queue)
}

// QueueCapacity 返回队列容量。
func (s *Service) QueueCapacity() int {
	if s == nil {
		return 0
	}
	return cap(s.queue)
}
