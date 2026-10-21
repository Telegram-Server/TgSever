package metrics

import (
	"math"
	"time"
)

// 离线推送（APNs）指标。type 是推送类型（message、voip、read、delete…），
// result 是发送结果分类，均由 internal/app/push 给出固定取值，序列数有界。

// PushSendResult 记录一次发送尝试的结果，按推送类型与结果分类计数。
func (r *Registry) PushSendResult(kind, result string) {
	r.inc("telesrv_push_sent_total", Label{Name: "type", Value: kind}, Label{Name: "result", Value: result})
}

// PushQueueDropped 记录队列已满被丢弃的推送任务。
func (r *Registry) PushQueueDropped(kind string) {
	r.inc("telesrv_push_queue_dropped_total", Label{Name: "type", Value: kind})
}

// PushDeviceRemoved 记录被自动清理的设备登记（token 失效、用户不一致）。
func (r *Registry) PushDeviceRemoved(reason string) {
	r.inc("telesrv_push_devices_removed_total", Label{Name: "reason", Value: reason})
}

// PushGaugeSource 是推送状态类指标的来源，抓取时读取。
type PushGaugeSource struct {
	// Enabled 为真表示推送服务在运行（配置齐全且证书未过期）。
	Enabled bool
	// QueueLength 返回当前排队的任务数；nil 时不导出队列指标（推送停用）。
	QueueLength   func() int
	QueueCapacity int
	// CertNotAfter 是 APNs 证书到期时间；零值（.p8 认证或未配置）时不导出剩余天数。
	CertNotAfter time.Time
	// Now 是时钟；nil 时用 time.Now。
	Now func() time.Time
}

// PushGauges 返回推送状态的 GaugeProvider：是否启用、队列长度与容量、证书剩余有效天数
// （向下取整，过期后为负数）。
func PushGauges(src PushGaugeSource) GaugeProvider {
	now := src.Now
	if now == nil {
		now = time.Now
	}
	return func() []GaugeSample {
		enabled := 0.0
		if src.Enabled {
			enabled = 1
		}
		samples := []GaugeSample{{Name: "telesrv_push_enabled", Value: enabled}}
		if src.QueueLength != nil {
			samples = append(samples,
				GaugeSample{Name: "telesrv_push_queue_length", Value: float64(src.QueueLength())},
				GaugeSample{Name: "telesrv_push_queue_capacity", Value: float64(src.QueueCapacity)},
			)
		}
		if !src.CertNotAfter.IsZero() {
			days := math.Floor(src.CertNotAfter.Sub(now()).Hours() / 24)
			samples = append(samples, GaugeSample{Name: "telesrv_push_apns_certificate_days_left", Value: days})
		}
		return samples
	}
}
