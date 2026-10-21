// Package apns 是推送发送器的 APNs 实现（github.com/sideshow/apns2）：加载并校验认证材料，
// 按登记的 sandbox 标志选择开发或生产网关，把 push.Notification 映射成 APNs 请求。
//
// 本期只支持证书认证（.pem，证书和未加密私钥合在一个文件里）。.p8 token 认证、证书到期
// 告警与过期停用在 #102 里加到 New 的校验中。
package apns

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"

	"telesrv/internal/app/push"
)

// Config 是 APNs 发送器的部署配置（对应 TELESRV_APNS_* 环境变量）。
type Config struct {
	// Enabled（TELESRV_APNS_ENABLED）为假时不创建发送器，服务器照常运行、只是不发推送。
	Enabled bool
	// Topic（TELESRV_APNS_TOPIC）是 App 的 bundle id；VoIP 推送用 Topic + ".voip"。
	Topic string
	// CertFile（TELESRV_APNS_CERT_FILE）是证书 + 私钥合一的 PEM 文件。
	CertFile string
}

// oidAPNsTopics 是 Apple Push Services 证书里列出可推送 topic 的扩展。
var oidAPNsTopics = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 3, 6}

// oidSubjectUID 是证书 subject 的 UID（单 topic 老证书里就是 bundle id）。
var oidSubjectUID = asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}

// Sender 通过 APNs 发送推送，满足 push.Sender。
type Sender struct {
	topic       string
	production  *apns2.Client
	development *apns2.Client
}

var _ push.Sender = (*Sender)(nil)

// New 校验配置并创建发送器。未启用时返回 (nil, nil)。错误信息带上出问题的环境变量名。
func New(cfg Config) (*Sender, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	topic := strings.TrimSpace(cfg.Topic)
	if topic == "" {
		return nil, errors.New("TELESRV_APNS_TOPIC is required when TELESRV_APNS_ENABLED=true")
	}
	path := strings.TrimSpace(cfg.CertFile)
	if path == "" {
		return nil, errors.New("TELESRV_APNS_CERT_FILE is required when TELESRV_APNS_ENABLED=true")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("TELESRV_APNS_CERT_FILE: %w", err)
	}
	cert, err := certificate.FromPemBytes(data, "")
	if err != nil {
		return nil, fmt.Errorf("TELESRV_APNS_CERT_FILE: parse certificate and private key: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("TELESRV_APNS_CERT_FILE: parse certificate: %w", err)
	}
	topics := certificateTopics(leaf)
	if !slices.Contains(topics, topic) {
		return nil, fmt.Errorf("TELESRV_APNS_TOPIC %q is not covered by the certificate in TELESRV_APNS_CERT_FILE (topics: %s)", topic, strings.Join(topics, ", "))
	}
	cert.Leaf = leaf
	return &Sender{
		topic:       topic,
		production:  apns2.NewClient(cert).Production(),
		development: apns2.NewClient(cert).Development(),
	}, nil
}

// certificateTopics 返回证书可推送的 topic：优先读 Apple 的 topic 扩展
// （SEQUENCE 里交替出现 UTF8String topic 与描述类型的 SEQUENCE），没有扩展时退回 subject UID。
func certificateTopics(cert *x509.Certificate) []string {
	var topics []string
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(oidAPNsTopics) {
			continue
		}
		var seq asn1.RawValue
		if _, err := asn1.Unmarshal(ext.Value, &seq); err != nil {
			continue
		}
		rest := seq.Bytes
		for len(rest) > 0 {
			var item asn1.RawValue
			next, err := asn1.Unmarshal(rest, &item)
			if err != nil {
				break
			}
			rest = next
			if item.Class == asn1.ClassUniversal && item.Tag == asn1.TagUTF8String {
				topics = append(topics, string(item.Bytes))
			}
		}
	}
	if len(topics) > 0 {
		return topics
	}
	for _, name := range cert.Subject.Names {
		if name.Type.Equal(oidSubjectUID) {
			if uid, ok := name.Value.(string); ok && uid != "" {
				topics = append(topics, uid)
			}
		}
	}
	return topics
}

// Send 把推送发到 APNs。网关拒绝时 err 为 nil，状态码与原因在 Result 里（分类见 #101）。
func (s *Sender) Send(ctx context.Context, n push.Notification) (push.Result, error) {
	client := s.production
	if n.Sandbox {
		client = s.development
	}
	note := &apns2.Notification{
		DeviceToken: n.Token,
		Topic:       s.topic,
		Payload:     n.Payload,
		Priority:    n.Priority,
		CollapseID:  n.CollapseID,
		Expiration:  n.Expiration,
		PushType:    apns2.PushTypeAlert,
	}
	if n.Type == push.PushTypeVoIP {
		note.Topic = s.topic + ".voip"
		note.PushType = apns2.PushTypeVOIP
	}
	if n.ExpireImmediately {
		note.Expiration = time.Time{}
		client = withZeroExpiration(client)
	}
	resp, err := client.PushWithContext(ctx, note)
	if err != nil {
		return push.Result{}, err
	}
	return push.Result{StatusCode: resp.StatusCode, Reason: resp.Reason, ID: resp.ApnsID}, nil
}

// withZeroExpiration 返回一个只用于本次请求的 client 副本，请求头带 apns-expiration: 0。
// apns2 只在 Expiration 晚于 Unix 0 时才写这个头，表达不了 0（只投递一次、不存储）；
// 副本共用原 client 的底层 transport 与连接，只在发出前补上请求头。
func withZeroExpiration(client *apns2.Client) *apns2.Client {
	c := *client
	httpClient := http.Client{}
	if client.HTTPClient != nil {
		httpClient = *client.HTTPClient
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = zeroExpirationTransport{base: base}
	c.HTTPClient = &httpClient
	return &c
}

type zeroExpirationTransport struct {
	base http.RoundTripper
}

func (t zeroExpirationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("apns-expiration", "0")
	return t.base.RoundTrip(req)
}
