package apns

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"telesrv/internal/app/push"
)

var oidUserID = asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}

// writeTestCert 生成一张仿 Apple Push Services 的自签证书（topic 扩展 + subject UID），
// 证书与未加密私钥合在一个 PEM 文件里，返回文件路径。
func writeTestCert(t *testing.T, uid string, topics ...string) string {
	t.Helper()
	return writeTestCertUntil(t, time.Now().Add(365*24*time.Hour), uid, topics...)
}

// writeTestCertUntil 同 writeTestCert，证书有效期截止到 notAfter。
func writeTestCertUntil(t *testing.T, notAfter time.Time, uid string, topics ...string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Apple Push Services: " + uid,
			ExtraNames: []pkix.AttributeTypeAndValue{{Type: oidUserID, Value: uid}},
		},
		NotBefore: notAfter.Add(-2 * 365 * 24 * time.Hour),
		NotAfter:  notAfter,
	}
	if len(topics) > 0 {
		// Apple 的 topic 扩展：SEQUENCE { UTF8String topic, SEQUENCE { UTF8String type }, ... }
		var elems []asn1.RawValue
		for _, topic := range topics {
			b, _ := asn1.MarshalWithParams(topic, "utf8")
			elems = append(elems, asn1.RawValue{FullBytes: b})
			kind, _ := asn1.MarshalWithParams("app", "utf8")
			seq, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: kind})
			elems = append(elems, asn1.RawValue{FullBytes: seq})
		}
		var inner []byte
		for _, e := range elems {
			inner = append(inner, e.FullBytes...)
		}
		value, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: inner})
		tmpl.ExtraExtensions = []pkix.Extension{{Id: oidAPNsTopics, Value: value}}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = pem.Encode(&b, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "apns.pem")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewDisabledReturnsNoSender(t *testing.T) {
	sender, err := New(Config{Enabled: false, CertFile: "/does/not/exist"})
	if err != nil || sender != nil {
		t.Fatalf("New(disabled) = %v, %v; want nil, nil", sender, err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	good := writeTestCert(t, "co.kaka.tgpro", "co.kaka.tgpro", "co.kaka.tgpro.voip")
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"missing topic", Config{Enabled: true, CertFile: good}, "TELESRV_APNS_TOPIC"},
		{"missing cert", Config{Enabled: true, Topic: "co.kaka.tgpro"}, "TELESRV_APNS_CERT_FILE"},
		{"unreadable cert", Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: filepath.Join(t.TempDir(), "missing.pem")}, "TELESRV_APNS_CERT_FILE"},
		{"unparsable cert", Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: garbage}, "TELESRV_APNS_CERT_FILE"},
		{"topic mismatch", Config{Enabled: true, Topic: "com.example.other", CertFile: good}, "TELESRV_APNS_TOPIC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender, err := New(tc.cfg)
			if err == nil || sender != nil {
				t.Fatalf("New = %v, %v; want an error", sender, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %s", err, tc.want)
			}
		})
	}
	if sender, err := New(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: good}); err != nil || sender == nil {
		t.Fatalf("New(valid) = %v, %v", sender, err)
	}
	// 没有 topic 扩展的老式单 topic 证书：按 subject UID 判断。
	uidOnly := writeTestCert(t, "co.kaka.tgpro")
	if _, err := New(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: uidOnly}); err != nil {
		t.Fatalf("New(uid-only cert) = %v", err)
	}
	if _, err := New(Config{Enabled: true, Topic: "co.kaka.other", CertFile: uidOnly}); err == nil {
		t.Fatal("uid-only cert accepted a different topic")
	}
}

// 发送器只是 apns2 的薄封装：这里只核对请求头映射（topic、push type、优先级）与网关选择。
func TestSenderMapsNotificationToAPNsRequest(t *testing.T) {
	type seen struct {
		path, topic, pushType, priority, collapse string
		body                                      string
	}
	got := make(chan seen, 2)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- seen{r.URL.Path, r.Header.Get("apns-topic"), r.Header.Get("apns-push-type"), r.Header.Get("apns-priority"), r.Header.Get("apns-collapse-id"), string(body)}
		if strings.Contains(r.URL.Path, "deadbeef") {
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"reason":"Unregistered"}`)
			return
		}
		w.Header().Set("apns-id", "id-1")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	sender, err := New(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: writeTestCert(t, "co.kaka.tgpro", "co.kaka.tgpro")})
	if err != nil {
		t.Fatal(err)
	}
	sender.production.HTTPClient, sender.production.Host = srv.Client(), srv.URL
	sender.development.HTTPClient, sender.development.Host = srv.Client(), srv.URL+"/sandbox"

	res, err := sender.Send(context.Background(), push.Notification{Token: "abc123", Type: push.PushTypeAlert, Priority: push.PriorityHigh, Payload: []byte(`{"aps":{}}`)})
	if err != nil || !res.Sent() || res.ID != "id-1" {
		t.Fatalf("Send = %+v, %v; want sent", res, err)
	}
	if s := <-got; s.path != "/3/device/abc123" || s.topic != "co.kaka.tgpro" || s.pushType != "alert" || s.priority != "10" || s.body != `{"aps":{}}` {
		t.Fatalf("request = %+v", s)
	}
	res, err = sender.Send(context.Background(), push.Notification{Token: "deadbeef", Sandbox: true, Type: push.PushTypeVoIP, Priority: push.PriorityLow, CollapseID: "c1", Payload: []byte(`{}`)})
	if err != nil || res.Sent() || res.StatusCode != http.StatusGone || res.Reason != "Unregistered" {
		t.Fatalf("Send = %+v, %v; want 410 Unregistered", res, err)
	}
	if s := <-got; s.path != "/sandbox/3/device/deadbeef" || s.topic != "co.kaka.tgpro.voip" || s.pushType != "voip" || s.priority != "5" || s.collapse != "c1" {
		t.Fatalf("voip request = %+v", s)
	}
}

// apns2 只在 Expiration 晚于 Unix 0 时才发 apns-expiration，无法发 0；来电必须是 0（只投递一次、不存储）。
func TestSenderSendsZeroExpirationWhenAsked(t *testing.T) {
	got := make(chan []string, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Values("apns-expiration")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	sender, err := New(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: writeTestCert(t, "co.kaka.tgpro", "co.kaka.tgpro")})
	if err != nil {
		t.Fatal(err)
	}
	sender.production.HTTPClient, sender.production.Host = srv.Client(), srv.URL

	send := func(n push.Notification) []string {
		t.Helper()
		n.Token, n.Payload = "abc123", []byte(`{}`)
		if res, err := sender.Send(context.Background(), n); err != nil || !res.Sent() {
			t.Fatalf("Send = %+v, %v", res, err)
		}
		return <-got
	}
	if h := send(push.Notification{Type: push.PushTypeVoIP, ExpireImmediately: true, Expiration: time.Unix(1900000000, 0)}); len(h) != 1 || h[0] != "0" {
		t.Fatalf("apns-expiration = %q, want exactly 0", h)
	}
	if h := send(push.Notification{Type: push.PushTypeAlert}); len(h) != 0 {
		t.Fatalf("apns-expiration = %q, want none by default", h)
	}
	if h := send(push.Notification{Type: push.PushTypeAlert, Expiration: time.Unix(1900000000, 0)}); len(h) != 1 || h[0] != "1900000000" {
		t.Fatalf("apns-expiration = %q, want 1900000000", h)
	}
}
