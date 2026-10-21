package push

// MetricsSink 是按字符串标签计数的指标后端（internal/observability/metrics.Registry 满足它）。
type MetricsSink interface {
	PushSendResult(kind, result string)
	PushQueueDropped(kind string)
	PushDeviceRemoved(reason string)
}

// SinkMetrics 把推送服务的 Metrics 回调接到 MetricsSink：发送结果先归成 Classify 的四类
// （sent、invalid_token、retryable、permanent），避免 APNs 的 reason 原样变成标签。
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
	m.sink.PushSendResult(kind, Classify(result, err).String())
}

// 自动清理登记的原因（指标标签）。
const (
	RemovedInvalidToken = "invalid_token"
	RemovedUserMismatch = "user_mismatch"
)

// QueueLength 返回当前排队等待处理的任务数（两条 lane 合计，监控指标用）。
func (s *Service) QueueLength() int {
	if s == nil {
		return 0
	}
	return len(s.urgent) + len(s.bulk)
}

// QueueCapacity 返回队列容量（两条 lane 合计）。
func (s *Service) QueueCapacity() int {
	if s == nil {
		return 0
	}
	return cap(s.urgent) + cap(s.bulk)
}
