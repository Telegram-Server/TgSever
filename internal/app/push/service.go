// Package push 是离线推送业务服务：决定“发给谁、发不发、发什么”，再交给 Sender 发到 APNs。
//
// 调用方（outbox 分发器等）只调用按事件区分的方法（NewMessage …），方法本身不做 I/O：
// 只把任务放进进程内有界队列，由固定数量的 worker 查询设备登记、判断前台、渲染文案、
// 加密 payload 并发送。队列满时丢弃（推送是尽力而为，不持久化），绝不阻塞消息投递。
//
// 后续扩展点：
//   - 通知规则（静音、预览、静默、other_uids，#96）：NotifyPolicy 与 deliverMessage 里的设备过滤；
//   - 媒体文案（#97）：keys.go 的 messageAlert；
//   - 群组/频道扇出（#98）：NewMessage.Recipients 已是集合，Chat.Kind 选择 payload 字段；
//   - VoIP（#99）、已读/删除同步（#100）：新增事件方法，复用 enqueue 与 send；
//   - 结果分类、重试、失效清理（#101）：classify.go 的 Classify 与 send（所有事件都经 send 发出，
//     发送前核对登记 user_id，token 失效删除登记，可重试失败按 Config.MaxRetries 退避重试）；
//   - 指标（#102）：Metrics。
package push

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// DeviceLister 读取用户的设备登记（store.PushDeviceStore 满足它）。
type DeviceLister interface {
	ListPushDevicesByUser(ctx context.Context, userID int64) ([]domain.PushDevice, error)
}

// DeviceStore 是推送服务用到的设备登记存储（store.PushDeviceStore 满足它）：读取登记，
// 并删除判定失效的登记（token 失效、user 与授权不一致）。删除只针对读到的那一版登记，
// 期间客户端重新登记的新登记不受影响。
type DeviceStore interface {
	DeviceLister
	DeleteStalePushDevice(ctx context.Context, device domain.PushDevice) (bool, error)
}

// Presence 回答“某个授权此刻是否在前台”：该授权有活跃连接，且最近一次 updateStatus
// 报的是 offline=false。没有会话记录时按不在前台处理。按授权判断，同一用户的其他设备不影响。
type Presence interface {
	AuthorizationInForeground(authKeyID [8]byte) bool
}

// AuthorizationLookup 读取授权当前绑定的 user（store.AuthorizationStore 满足它）。
// 授权的 user_id 可能被原地改写（扫码登录接受时），发送前用它核对登记里的 user_id。
type AuthorizationLookup interface {
	ByAuthKey(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error)
}

// UserLookup 读取用户资料（rpc.UsersService 满足它），用于渲染发送者名字。
type UserLookup interface {
	ByID(ctx context.Context, currentUserID, userID int64) (domain.User, bool, error)
}

// NotifyDecision 是某个收件人对某个会话此刻的通知设置判定。
type NotifyDecision struct {
	// Mute 为真时不推送。
	Mute bool
	// HidePreview 为真时不带消息内容（*_NOTEXT）。
	HidePreview bool
	// Silent 为真时不带提示音。
	Silent bool
}

// NotifyPolicy 按收件人的通知设置决定是否推送、是否带内容（#96 实现）。未注入时一律推送、带预览。
type NotifyPolicy interface {
	Decide(ctx context.Context, recipientUserID int64, chat Chat) (NotifyDecision, error)
}

// Metrics 记录推送计数（#102 接到 Prometheus 注册表）。
type Metrics interface {
	PushQueueDropped(kind string)
	PushSent(kind string, result Result, err error)
}

type nopMetrics struct{}

func (nopMetrics) PushQueueDropped(string)        {}
func (nopMetrics) PushSent(string, Result, error) {}

// Config 是推送服务的运行参数；零值字段取默认值。
type Config struct {
	// QueueSize 是待处理任务队列的容量，满了就丢弃。
	QueueSize int
	// Workers 是固定 worker 数。
	Workers int
	// SendTimeout 是单次发送（一台设备）的超时。
	SendTimeout time.Duration
	// MaxRetries 是可重试失败（限流、网关 5xx、超时）之后最多再发几次；0 取默认值，负数不重试。
	MaxRetries int
	// RetryBackoff 是第一次重试前的等待，之后每次翻倍，最长 maxRetryBackoff。
	RetryBackoff time.Duration
}

const (
	defaultQueueSize    = 4096
	defaultWorkers      = 4
	defaultSendTimeout  = 10 * time.Second
	defaultMaxRetries   = 3
	defaultRetryBackoff = 500 * time.Millisecond
	maxRetryBackoff     = 5 * time.Second
)

// Deps 是推送服务的依赖。Sender 为 nil 时服务处于停用状态：所有事件方法直接返回 false。
type Deps struct {
	Devices DeviceStore
	// Authorizations 用于发送前核对登记的 user_id；nil 时不核对。
	Authorizations AuthorizationLookup
	Presence       Presence
	Users          UserLookup
	Sender         Sender
	// Strings 是 iOS 语言包来源；nil 时只用内置英文模板。
	Strings LangPackSource
	Policy  NotifyPolicy
	Metrics Metrics
}

// NewMessage 是一条新消息事件：Recipients 中每个收件人以 Chat（收件人视角的会话）看到这条消息。
// 私聊每个收件人的 msg_id 不同（各自的 message box），因此私聊每次只带一个收件人。
type NewMessage struct {
	Chat       Chat
	SenderID   int64
	Recipients []int64
	MessageID  int
	Text       string
	Media      *domain.MessageMedia
	// Silent 是发送方以静默方式发送（message.silent）。
	Silent bool
}

type job struct {
	kind string
	run  func(ctx context.Context)
}

// Service 是推送业务服务。
type Service struct {
	cfg      Config
	devices  DeviceStore
	auths    AuthorizationLookup
	presence Presence
	users    UserLookup
	sender   Sender
	render   *Renderer
	policy   NotifyPolicy
	metrics  Metrics
	log      *zap.Logger
	queue    chan job
}

// NewService 创建推送服务；调用 Run 启动 worker。
func NewService(cfg Config, deps Deps, log *zap.Logger) *Service {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = defaultSendTimeout
	}
	switch {
	case cfg.MaxRetries == 0:
		cfg.MaxRetries = defaultMaxRetries
	case cfg.MaxRetries < 0:
		cfg.MaxRetries = 0
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	if log == nil {
		log = zap.NewNop()
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = nopMetrics{}
	}
	return &Service{
		cfg:      cfg,
		devices:  deps.Devices,
		auths:    deps.Authorizations,
		presence: deps.Presence,
		users:    deps.Users,
		sender:   deps.Sender,
		render:   NewRenderer(deps.Strings),
		policy:   deps.Policy,
		metrics:  metrics,
		log:      log,
		queue:    make(chan job, cfg.QueueSize),
	}
}

// Enabled 报告服务是否会发推送（配置了发送器与设备存储）。
func (s *Service) Enabled() bool {
	return s != nil && s.sender != nil && s.devices != nil
}

// Run 启动 cfg.Workers 个 worker 并阻塞到 ctx 结束；结束时处理完队列里已有的任务再返回。
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		<-ctx.Done()
		return
	}
	done := make(chan struct{})
	for range s.cfg.Workers {
		go func() {
			defer func() { done <- struct{}{} }()
			s.work(ctx)
		}()
	}
	for range s.cfg.Workers {
		<-done
	}
}

func (s *Service) work(ctx context.Context) {
	// 发送用的 ctx 不随 Run 的 ctx 取消：关停时队列里已有的任务仍按各自的发送超时处理完。
	jobCtx := context.WithoutCancel(ctx)
	for {
		select {
		case j := <-s.queue:
			s.runJob(jobCtx, j)
		case <-ctx.Done():
			for {
				select {
				case j := <-s.queue:
					s.runJob(jobCtx, j)
				default:
					return
				}
			}
		}
	}
}

func (s *Service) runJob(ctx context.Context, j job) {
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("push job panicked", zap.String("kind", j.kind), zap.Any("panic", rec))
		}
	}()
	j.run(ctx)
}

// enqueue 非阻塞入队；队列满或服务停用时返回 false。
func (s *Service) enqueue(kind string, run func(ctx context.Context)) bool {
	if !s.Enabled() {
		return false
	}
	select {
	case s.queue <- job{kind: kind, run: run}:
		return true
	default:
		s.metrics.PushQueueDropped(kind)
		return false
	}
}

// NewMessage 把一条新消息的推送放进队列，立即返回；返回 false 表示服务停用或队列已满被丢弃。
func (s *Service) NewMessage(msg NewMessage) bool {
	msg.Recipients = append([]int64(nil), msg.Recipients...)
	return s.enqueue("message", func(ctx context.Context) { s.deliverMessage(ctx, msg) })
}

func (s *Service) deliverMessage(ctx context.Context, msg NewMessage) {
	for _, recipient := range msg.Recipients {
		if recipient == 0 || recipient == msg.SenderID {
			continue
		}
		s.deliverMessageTo(ctx, recipient, msg)
	}
}

func (s *Service) deliverMessageTo(ctx context.Context, recipient int64, msg NewMessage) {
	devices, err := s.devices.ListPushDevicesByUser(ctx, recipient)
	if err != nil {
		s.log.Warn("list push devices failed", zap.Int64("user_id", recipient), zap.Error(err))
		return
	}
	targets := devices[:0]
	for _, device := range devices {
		if device.TokenType != domain.PushTokenAPNs || device.UserID != recipient {
			continue
		}
		if s.presence != nil && s.presence.AuthorizationInForeground(device.AuthKeyID) {
			continue
		}
		targets = append(targets, device)
	}
	if len(targets) == 0 {
		return
	}
	decision := NotifyDecision{}
	if s.policy != nil {
		decision, err = s.policy.Decide(ctx, recipient, msg.Chat)
		if err != nil {
			s.log.Warn("push notify policy failed", zap.Int64("user_id", recipient), zap.Error(err))
			return
		}
	}
	if decision.Mute {
		return
	}
	senderName := s.displayName(ctx, recipient, msg.SenderID)
	key, args := messageAlert(msg, senderName, !decision.HidePreview)
	silent := msg.Silent || decision.Silent
	for _, device := range targets {
		if len(device.Secret) != 0 && len(device.Secret) != SecretSize {
			// 不能退回明文：那样会把内容暴露给 Apple。
			s.log.Warn("push device secret has unexpected length; skipped",
				zap.Int64("user_id", recipient), zap.String("token", RedactToken(device.Token)), zap.Int("secret_len", len(device.Secret)))
			continue
		}
		content := MessagePush{
			Chat:      msg.Chat,
			MessageID: msg.MessageID,
			Key:       key,
			Alert:     s.render.Render(ctx, device.LangCode, key, args...),
			Silent:    silent,
		}
		generic := s.render.Render(ctx, device.LangCode, KeyEncryptedMessage, "").Body
		payload, err := buildMessagePayload(content, device.Secret, generic)
		if err != nil {
			s.log.Warn("build push payload failed", zap.Int64("user_id", recipient), zap.String("token", RedactToken(device.Token)), zap.Error(err))
			continue
		}
		s.send(ctx, "message", device, Notification{
			Token:    device.Token,
			Sandbox:  device.AppSandbox,
			Type:     PushTypeAlert,
			Priority: PriorityHigh,
			Payload:  payload,
		})
	}
}

// send 发出一条推送并按结果分类（Classify）处理：token 失效删除登记；可重试失败按
// cfg.MaxRetries 有限次退避重试，用完后放弃；永久失败只记日志。
// 日志里 token 只记前几位，不记 payload 与 secret。
func (s *Service) send(ctx context.Context, kind string, device domain.PushDevice, n Notification) {
	if !s.registrationOwnerMatches(ctx, device) {
		return
	}
	result, err := s.sendOnce(ctx, kind, n)
	outcome := Classify(result, err)
	backoff := s.cfg.RetryBackoff
	for attempt := 1; outcome == OutcomeRetryable && attempt <= s.cfg.MaxRetries; attempt++ {
		s.log.Debug("push retry", zap.String("kind", kind), zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(n.Token)), zap.Int("attempt", attempt), zap.Int("status", result.StatusCode),
			zap.String("reason", result.Reason), zap.Error(err), zap.Duration("backoff", backoff))
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		backoff = min(backoff*2, maxRetryBackoff)
		result, err = s.sendOnce(ctx, kind, n)
		outcome = Classify(result, err)
	}
	switch outcome {
	case OutcomeSent:
		s.log.Debug("push sent", zap.String("kind", kind), zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(n.Token)), zap.String("apns_id", result.ID))
	case OutcomeInvalidToken:
		s.deleteStale(ctx, device, "invalid token: "+result.Reason)
	default:
		s.log.Warn("push failed", zap.String("kind", kind), zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(n.Token)), zap.Int("status", result.StatusCode),
			zap.String("reason", result.Reason), zap.Error(err))
	}
}

// registrationOwnerMatches 核对登记的 user_id 与授权当前的 user：不一致（或授权已不存在）时
// 删除这条登记并返回 false。读授权失败时这次不发，但保留登记。
func (s *Service) registrationOwnerMatches(ctx context.Context, device domain.PushDevice) bool {
	if s.auths == nil {
		return true
	}
	auth, found, err := s.auths.ByAuthKey(ctx, device.AuthKeyID)
	if err != nil {
		s.log.Warn("push authorization lookup failed", zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(device.Token)), zap.Error(err))
		return false
	}
	if found && auth.UserID == device.UserID {
		return true
	}
	s.deleteStale(ctx, device, "authorization user changed")
	return false
}

// sendOnce 带单次发送超时发一次，并记入指标。
func (s *Service) sendOnce(ctx context.Context, kind string, n Notification) (Result, error) {
	sendCtx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer cancel()
	result, err := s.sender.Send(sendCtx, n)
	s.metrics.PushSent(kind, result, err)
	return result, err
}

// deleteStale 删除判定失效的登记（只删读到的那一版，见 DeviceStore）。
func (s *Service) deleteStale(ctx context.Context, device domain.PushDevice, why string) {
	deleted, err := s.devices.DeleteStalePushDevice(ctx, device)
	if err != nil {
		s.log.Warn("delete stale push device failed", zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(device.Token)), zap.String("why", why), zap.Error(err))
		return
	}
	s.log.Info("stale push device removed", zap.Int64("user_id", device.UserID),
		zap.String("token", RedactToken(device.Token)), zap.String("why", why), zap.Bool("deleted", deleted))
}

// displayName 返回收件人视角下发送者的显示名；读不到时返回空串（文案照常渲染）。
func (s *Service) displayName(ctx context.Context, viewerID, userID int64) string {
	if s.users == nil || userID == 0 {
		return ""
	}
	u, found, err := s.users.ByID(ctx, viewerID, userID)
	if err != nil || !found {
		return ""
	}
	return UserDisplayName(u)
}

// UserDisplayName 拼出用户的显示名：名 + 姓，都为空时退回用户名。
func UserDisplayName(u domain.User) string {
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name == "" {
		name = u.Username
	}
	return name
}

// RedactToken 只保留 token 前 8 位，用于日志。
func RedactToken(token string) string {
	const keep = 8
	if len(token) <= keep {
		return token[:min(len(token), 4)] + "…"
	}
	return token[:keep] + "…"
}
