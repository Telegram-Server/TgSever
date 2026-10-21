package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/app/push"
)

var _ push.MetricsSink = (*Registry)(nil)

func scrape(t *testing.T, registry *Registry) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	registry.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	return recorder.Body.String()
}

func TestRegistryExportsPushCounters(t *testing.T) {
	registry := New()
	registry.PushSendResult("message", "sent")
	registry.PushSendResult("message", "sent")
	registry.PushSendResult("message", "invalid_token")
	registry.PushSendResult("voip", "retryable")
	registry.PushQueueDropped("message")
	registry.PushDeviceRemoved("invalid_token")
	body := scrape(t, registry)
	for _, want := range []string{
		`telesrv_push_sent_total{type="message",result="sent"} 2`,
		`telesrv_push_sent_total{type="message",result="invalid_token"} 1`,
		`telesrv_push_sent_total{type="voip",result="retryable"} 1`,
		`telesrv_push_queue_dropped_total{type="message"} 1`,
		`telesrv_push_devices_removed_total{reason="invalid_token"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q from:\n%s", want, body)
		}
	}
}

func TestPushGaugesReportQueueAndCertificate(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	queued := 3
	registry := New()
	registry.AddGaugeProvider(PushGauges(PushGaugeSource{
		Enabled:       true,
		QueueLength:   func() int { return queued },
		QueueCapacity: 4096,
		CertNotAfter:  now.Add(10*24*time.Hour + 5*time.Hour),
		Now:           func() time.Time { return now },
	}))
	body := scrape(t, registry)
	for _, want := range []string{
		"telesrv_push_enabled 1",
		"telesrv_push_queue_length 3",
		"telesrv_push_queue_capacity 4096",
		"telesrv_push_apns_certificate_days_left 10",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q from:\n%s", want, body)
		}
	}
	queued = 7
	if body := scrape(t, registry); !strings.Contains(body, "telesrv_push_queue_length 7") {
		t.Fatalf("queue length is not read at scrape time:\n%s", body)
	}
}

func TestPushGaugesExpiredCertificateAndTokenAuth(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	// 证书已过期、推送被停用：没有队列，剩余天数为负，enabled 为 0。
	registry := New()
	registry.AddGaugeProvider(PushGauges(PushGaugeSource{
		CertNotAfter: now.Add(-36 * time.Hour),
		Now:          func() time.Time { return now },
	}))
	body := scrape(t, registry)
	for _, want := range []string{"telesrv_push_enabled 0", "telesrv_push_apns_certificate_days_left -2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q from:\n%s", want, body)
		}
	}
	if strings.Contains(body, "telesrv_push_queue_length") {
		t.Fatalf("disabled push reports a queue:\n%s", body)
	}
	// .p8 认证没有证书到期时间：不导出剩余天数。
	registry = New()
	registry.AddGaugeProvider(PushGauges(PushGaugeSource{Enabled: true, QueueLength: func() int { return 0 }, QueueCapacity: 1}))
	if body := scrape(t, registry); strings.Contains(body, "certificate_days_left") {
		t.Fatalf("token auth reports certificate days:\n%s", body)
	}
}
