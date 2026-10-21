// Package apns 是推送发送器的 APNs 实现（github.com/sideshow/apns2）：加载并校验认证材料，
// 按登记的 sandbox 标志选择开发或生产网关，把 push.Notification 映射成 APNs 请求。
//
// 两种认证方式二选一：
//   - 证书（.pem，证书和未加密私钥合在一个文件里）：校验 topic 覆盖 bundle id、检查有效期；
//     证书不覆盖 bundle id + ".voip" 时只停用 VoIP（启动记警告），普通推送照常；
//   - .p8 token（Key ID + Team ID + PKCS#8 私钥）：签发 JWT，不会过期，topic 由 Apple 按 Team 授权。
//
// 证书已过期不算配置错误：Open 记错误日志并停用推送，服务器照常启动；剩余不足 30 天记警告。
package apns

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/certificate"
	"github.com/sideshow/apns2/token"
	"go.uber.org/zap"

	"telesrv/internal/app/push"
)

// ExpiryWarningWindow 是证书到期告警的提前量：剩余有效期不足它时启动日志记警告。
const ExpiryWarningWindow = 30 * 24 * time.Hour

// Config 是 APNs 发送器的部署配置（对应 TELESRV_APNS_* 环境变量）。
type Config struct {
	// Enabled（TELESRV_APNS_ENABLED）为假时不创建发送器，服务器照常运行、只是不发推送。
	Enabled bool
	// Topic（TELESRV_APNS_TOPIC）是 App 的 bundle id；VoIP 推送用 Topic + ".voip"。
	Topic string
	// CertFile（TELESRV_APNS_CERT_FILE）是证书 + 私钥合一的 PEM 文件。和 KeyFile 二选一。
	CertFile string
	// KeyFile（TELESRV_APNS_KEY_FILE）是 Apple 开发者后台下载的 .p8 认证密钥。和 CertFile 二选一。
	KeyFile string
	// KeyID（TELESRV_APNS_KEY_ID）是 .p8 密钥的 10 位 Key ID。
	KeyID string
	// TeamID（TELESRV_APNS_TEAM_ID）是开发者账号的 10 位 Team ID。
	TeamID string
	// Now 是检查证书有效期用的时钟；nil 时用 time.Now（测试注入）。
	Now func() time.Time
}

// CertificateExpiredError 表示配置的推送证书已经过期。Open 把它当作“停用推送”而不是启动失败。
type CertificateExpiredError struct {
	NotAfter time.Time
}

func (e *CertificateExpiredError) Error() string {
	return fmt.Sprintf("TELESRV_APNS_CERT_FILE: certificate expired at %s", e.NotAfter.UTC().Format(time.RFC3339))
}

// Status 是启动时认证材料的状态，供日志与监控指标使用。
type Status struct {
	// CertNotAfter 是证书到期时间；.p8 认证或未启用时为零值。证书过期被停用时也会填上。
	CertNotAfter time.Time
}

// appleID 匹配 Apple 的 Key ID / Team ID：10 位大写字母或数字。
var appleID = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// oidAPNsTopics 是 Apple Push Services 证书里列出可推送 topic 的扩展。
var oidAPNsTopics = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 3, 6}

// oidSubjectUID 是证书 subject 的 UID（单 topic 老证书里就是 bundle id）。
var oidSubjectUID = asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}

// Sender 通过 APNs 发送推送，满足 push.Sender。
type Sender struct {
	topic       string
	production  *apns2.Client
	development *apns2.Client
	// certNotAfter 是证书到期时间；.p8 认证时为零值。
	certNotAfter time.Time
	// voipDisabled 为真时证书不覆盖 VoIP topic：VoIP 推送不发往网关（返回 ErrVoIPDisabled）。
	voipDisabled bool
}

var (
	_ push.Sender      = (*Sender)(nil)
	_ push.VoIPCapable = (*Sender)(nil)
)

// ErrVoIPDisabled 表示证书不覆盖 VoIP topic，VoIP 推送已停用。
var ErrVoIPDisabled = errors.New("apns: certificate does not cover the voip topic; voip push disabled")

// VoIPEnabled 报告能否发 VoIP 推送：.p8 认证恒为真（topic 由 Apple 按 Team 授权，本地无从检查），
// 证书认证要求证书覆盖 topic + ".voip"。
func (s *Sender) VoIPEnabled() bool {
	return s != nil && !s.voipDisabled
}

// New 校验配置并创建发送器。未启用时返回 (nil, nil)。错误信息带上出问题的环境变量名。
// 证书已过期时返回 *CertificateExpiredError（用 Open 把它变成“停用推送”）。
func New(cfg Config) (*Sender, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	topic := strings.TrimSpace(cfg.Topic)
	if topic == "" {
		return nil, errors.New("TELESRV_APNS_TOPIC is required when TELESRV_APNS_ENABLED=true")
	}
	certFile := strings.TrimSpace(cfg.CertFile)
	keyFile := strings.TrimSpace(cfg.KeyFile)
	keyID := strings.TrimSpace(cfg.KeyID)
	teamID := strings.TrimSpace(cfg.TeamID)
	switch {
	case certFile != "" && keyFile != "":
		return nil, errors.New("set TELESRV_APNS_CERT_FILE or TELESRV_APNS_KEY_FILE, not both")
	case certFile != "":
		if keyID != "" || teamID != "" {
			return nil, errors.New("TELESRV_APNS_KEY_ID/TELESRV_APNS_TEAM_ID only apply to TELESRV_APNS_KEY_FILE (.p8); remove them when using TELESRV_APNS_CERT_FILE")
		}
		now := time.Now
		if cfg.Now != nil {
			now = cfg.Now
		}
		return newCertSender(topic, certFile, now())
	case keyFile != "":
		return newTokenSender(topic, keyFile, keyID, teamID)
	default:
		return nil, errors.New("TELESRV_APNS_CERT_FILE or TELESRV_APNS_KEY_FILE is required when TELESRV_APNS_ENABLED=true")
	}
}

// Open 是启动入口：校验配置并创建发送器，配置错误返回 error（启动失败）。
// 证书已过期时记错误日志、返回 nil 发送器（推送停用，服务器照常启动）；
// 剩余不足 ExpiryWarningWindow 时记警告日志。未启用时返回 (nil, Status{}, nil)。
func Open(cfg Config, log *zap.Logger) (*Sender, Status, error) {
	if log == nil {
		log = zap.NewNop()
	}
	sender, err := New(cfg)
	var expired *CertificateExpiredError
	if errors.As(err, &expired) {
		log.Error("apns certificate expired; push disabled",
			zap.Time("not_after", expired.NotAfter), zap.String("cert_file", cfg.CertFile))
		return nil, Status{CertNotAfter: expired.NotAfter}, nil
	}
	if err != nil || sender == nil {
		return nil, Status{}, err
	}
	status := Status{CertNotAfter: sender.certNotAfter}
	if !sender.VoIPEnabled() {
		log.Warn("apns certificate does not cover the voip topic; voip push disabled",
			zap.String("voip_topic", sender.topic+".voip"), zap.String("cert_file", cfg.CertFile))
	}
	if notAfter, ok := sender.CertificateNotAfter(); ok {
		now := time.Now
		if cfg.Now != nil {
			now = cfg.Now
		}
		if left := notAfter.Sub(now()); left < ExpiryWarningWindow {
			log.Warn("apns certificate expires soon",
				zap.Time("not_after", notAfter), zap.Int("days_left", int(left/(24*time.Hour))), zap.String("cert_file", cfg.CertFile))
		}
	}
	return sender, status, nil
}

// CertificateNotAfter 返回证书到期时间；.p8 认证没有到期时间，返回 false。
func (s *Sender) CertificateNotAfter() (time.Time, bool) {
	if s == nil || s.certNotAfter.IsZero() {
		return time.Time{}, false
	}
	return s.certNotAfter, true
}

func newTokenSender(topic, keyFile, keyID, teamID string) (*Sender, error) {
	if keyID == "" {
		return nil, errors.New("TELESRV_APNS_KEY_ID is required with TELESRV_APNS_KEY_FILE")
	}
	if !appleID.MatchString(keyID) {
		return nil, fmt.Errorf("TELESRV_APNS_KEY_ID %q must be the 10-character Key ID of the .p8 key", keyID)
	}
	if teamID == "" {
		return nil, errors.New("TELESRV_APNS_TEAM_ID is required with TELESRV_APNS_KEY_FILE")
	}
	if !appleID.MatchString(teamID) {
		return nil, fmt.Errorf("TELESRV_APNS_TEAM_ID %q must be the 10-character Apple Developer Team ID", teamID)
	}
	key, err := token.AuthKeyFromFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("TELESRV_APNS_KEY_FILE: load .p8 key: %w", err)
	}
	// 两个网关共用同一个 token：JWT 与网关无关，apns2 内部加锁并在到期前重签。
	tok := &token.Token{AuthKey: key, KeyID: keyID, TeamID: teamID}
	return &Sender{
		topic:       topic,
		production:  apns2.NewTokenClient(tok).Production(),
		development: apns2.NewTokenClient(tok).Development(),
	}, nil
}

func newCertSender(topic, path string, now time.Time) (*Sender, error) {
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
	if !now.Before(leaf.NotAfter) {
		return nil, &CertificateExpiredError{NotAfter: leaf.NotAfter}
	}
	cert.Leaf = leaf
	return &Sender{
		topic:        topic,
		production:   apns2.NewClient(cert).Production(),
		development:  apns2.NewClient(cert).Development(),
		certNotAfter: leaf.NotAfter,
		voipDisabled: !slices.Contains(topics, topic+".voip"),
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
		if s.voipDisabled {
			return push.Result{}, ErrVoIPDisabled
		}
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
