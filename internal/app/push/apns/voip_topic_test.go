package apns

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"telesrv/internal/app/push"
)

// 证书不覆盖 bundle id + ".voip" 时只停用 VoIP：启动记警告，普通推送照常，VoIP 不发到网关。
func TestOpenCertificateWithoutVoIPTopicDisablesOnlyVoIP(t *testing.T) {
	cases := []struct {
		name     string
		topics   []string
		wantVoIP bool
	}{
		{"covers voip", []string{"co.kaka.tgpro", "co.kaka.tgpro.voip"}, true},
		{"bundle only", []string{"co.kaka.tgpro"}, false},
		{"legacy uid only", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			cert := writeTestCert(t, "co.kaka.tgpro", tc.topics...)
			sender, _, err := Open(Config{Enabled: true, Topic: "co.kaka.tgpro", CertFile: cert}, zap.New(core))
			if err != nil || sender == nil {
				t.Fatalf("Open = %v, %v; want a sender for regular push", sender, err)
			}
			if got := sender.VoIPEnabled(); got != tc.wantVoIP {
				t.Fatalf("VoIPEnabled = %v, want %v", got, tc.wantVoIP)
			}
			warned := logs.FilterMessage("apns certificate does not cover the voip topic; voip push disabled").Len() == 1
			if warned == tc.wantVoIP {
				t.Fatalf("voip warning logged = %v, want %v (logs %+v)", warned, !tc.wantVoIP, logs.All())
			}
			if !tc.wantVoIP {
				// 网关地址指向不存在的主机：若真发出去会得到网络错误而不是 ErrVoIPDisabled。
				sender.production.Host = "https://127.0.0.1:1"
				if _, err := sender.Send(context.Background(), push.Notification{Token: "abc", Type: push.PushTypeVoIP, Payload: []byte(`{}`)}); err != ErrVoIPDisabled {
					t.Fatalf("Send(voip) err = %v, want ErrVoIPDisabled", err)
				}
			}
		})
	}
}

func TestTokenSenderAllowsVoIP(t *testing.T) {
	sender, err := New(Config{Enabled: true, Topic: "co.kaka.tgpro", KeyFile: writeTestP8(t), KeyID: "ABC123DEFG", TeamID: "DEF123GHIJ"})
	if err != nil {
		t.Fatal(err)
	}
	if !sender.VoIPEnabled() {
		t.Fatal("token (.p8) sender VoIPEnabled = false, want true: Apple authorises topics per team")
	}
}
