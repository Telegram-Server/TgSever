package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// writeTestP8 生成一把仿 Apple 的 .p8 密钥（PKCS#8 封装的 P-256 私钥），返回文件路径。
func writeTestP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_ABC123DEFG.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewValidatesAuthMethod(t *testing.T) {
	cert := writeTestCert(t, "co.kaka.tgpro", "co.kaka.tgpro")
	p8 := writeTestP8(t)
	// 证书 PEM 冒充 .p8：第一块是证书而不是 PKCS#8 私钥。
	notKey := filepath.Join(t.TempDir(), "cert-as.p8")
	data, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notKey, data, 0o600); err != nil {
		t.Fatal(err)
	}
	const topic = "co.kaka.tgpro"
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"neither", Config{Enabled: true, Topic: topic}, "TELESRV_APNS_CERT_FILE or TELESRV_APNS_KEY_FILE"},
		{"both", Config{Enabled: true, Topic: topic, CertFile: cert, KeyFile: p8, KeyID: "ABC123DEFG", TeamID: "TEAM123456"}, "not both"},
		{"p8 missing key id", Config{Enabled: true, Topic: topic, KeyFile: p8, TeamID: "TEAM123456"}, "TELESRV_APNS_KEY_ID"},
		{"p8 missing team id", Config{Enabled: true, Topic: topic, KeyFile: p8, KeyID: "ABC123DEFG"}, "TELESRV_APNS_TEAM_ID"},
		{"p8 malformed key id", Config{Enabled: true, Topic: topic, KeyFile: p8, KeyID: "abc", TeamID: "TEAM123456"}, "TELESRV_APNS_KEY_ID"},
		{"p8 malformed team id", Config{Enabled: true, Topic: topic, KeyFile: p8, KeyID: "ABC123DEFG", TeamID: "team 1"}, "TELESRV_APNS_TEAM_ID"},
		{"p8 ids without key file", Config{Enabled: true, Topic: topic, CertFile: cert, KeyID: "ABC123DEFG"}, "TELESRV_APNS_KEY_FILE"},
		{"p8 unreadable", Config{Enabled: true, Topic: topic, KeyFile: filepath.Join(t.TempDir(), "missing.p8"), KeyID: "ABC123DEFG", TeamID: "TEAM123456"}, "TELESRV_APNS_KEY_FILE"},
		{"p8 not a key", Config{Enabled: true, Topic: topic, KeyFile: notKey, KeyID: "ABC123DEFG", TeamID: "TEAM123456"}, "TELESRV_APNS_KEY_FILE"},
		{"p8 missing topic", Config{Enabled: true, KeyFile: p8, KeyID: "ABC123DEFG", TeamID: "TEAM123456"}, "TELESRV_APNS_TOPIC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender, err := New(tc.cfg)
			if err == nil || sender != nil {
				t.Fatalf("New = %v, %v; want an error", sender, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	sender, err := New(Config{Enabled: true, Topic: topic, KeyFile: p8, KeyID: "ABC123DEFG", TeamID: "TEAM123456"})
	if err != nil || sender == nil {
		t.Fatalf("New(p8) = %v, %v", sender, err)
	}
	for _, client := range []*struct{ id, team string }{
		{sender.production.Token.KeyID, sender.production.Token.TeamID},
		{sender.development.Token.KeyID, sender.development.Token.TeamID},
	} {
		if client.id != "ABC123DEFG" || client.team != "TEAM123456" {
			t.Fatalf("p8 sender token = %+v", client)
		}
	}
	if _, ok := sender.CertificateNotAfter(); ok {
		t.Fatal(".p8 sender reports a certificate expiry")
	}
}

func TestNewRejectsExpiredCertificate(t *testing.T) {
	notAfter := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cert := writeTestCertUntil(t, notAfter, "co.kaka.tgpro", "co.kaka.tgpro")
	sender, err := New(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: cert, Now: func() time.Time { return notAfter.Add(time.Minute) }})
	var expired *CertificateExpiredError
	if sender != nil || !errors.As(err, &expired) || !expired.NotAfter.Equal(notAfter) {
		t.Fatalf("New(expired) = %v, %v; want CertificateExpiredError at %s", sender, err, notAfter)
	}
	if !strings.Contains(err.Error(), "TELESRV_APNS_CERT_FILE") {
		t.Fatalf("error %q does not name TELESRV_APNS_CERT_FILE", err)
	}
	sender, err = New(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: cert, Now: func() time.Time { return notAfter.Add(-time.Minute) }})
	if err != nil {
		t.Fatalf("New(not yet expired) = %v", err)
	}
	if got, ok := sender.CertificateNotAfter(); !ok || !got.Equal(notAfter) {
		t.Fatalf("CertificateNotAfter = %s, %v; want %s", got, ok, notAfter)
	}
}

func TestOpenCertificateExpiry(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		notAfter    time.Time
		wantSender  bool
		wantLevel   zapcore.Level
		wantMessage string
	}{
		{"healthy", now.Add(31 * 24 * time.Hour), true, zapcore.InfoLevel, ""},
		{"expiring soon", now.Add(29 * 24 * time.Hour), true, zapcore.WarnLevel, "apns certificate expires soon"},
		{"expired", now.Add(-time.Hour), false, zapcore.ErrorLevel, "apns certificate expired; push disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cert := writeTestCertUntil(t, tc.notAfter, "co.kaka.tgpro", "co.kaka.tgpro")
			core, logs := observer.New(zap.InfoLevel)
			sender, status, err := Open(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: cert, Now: func() time.Time { return now }}, zap.New(core))
			if err != nil {
				t.Fatalf("Open = %v; an expired or expiring certificate must not stop startup", err)
			}
			if (sender != nil) != tc.wantSender {
				t.Fatalf("Open sender = %v; want sender %v", sender, tc.wantSender)
			}
			if !status.CertNotAfter.Equal(tc.notAfter) {
				t.Fatalf("status.CertNotAfter = %s; want %s", status.CertNotAfter, tc.notAfter)
			}
			var found bool
			for _, entry := range logs.All() {
				if entry.Level < zapcore.WarnLevel {
					continue
				}
				if tc.wantMessage == "" {
					t.Fatalf("unexpected %s log %q", entry.Level, entry.Message)
				}
				if entry.Level == tc.wantLevel && entry.Message == tc.wantMessage {
					found = true
				}
			}
			if tc.wantMessage != "" && !found {
				t.Fatalf("missing %s log %q in %+v", tc.wantLevel, tc.wantMessage, logs.All())
			}
		})
	}
}

func TestOpenPropagatesMisconfiguration(t *testing.T) {
	sender, _, err := Open(Config{Enabled: true, Topic: "co.kaka.tgpro"}, zap.NewNop())
	if err == nil || sender != nil {
		t.Fatalf("Open(no credentials) = %v, %v; want an error", sender, err)
	}
	sender, status, err := Open(Config{}, zap.NewNop())
	if err != nil || sender != nil || !status.CertNotAfter.IsZero() {
		t.Fatalf("Open(disabled) = %v, %+v, %v", sender, status, err)
	}
}
