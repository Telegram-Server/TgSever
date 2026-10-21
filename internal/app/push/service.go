// Package push 是离线推送业务服务：决定“发给谁、发不发、发什么”，再交给 Sender 发到 APNs。
//
// 调用方（outbox 分发器等）只调用按事件区分的方法（NewMessage …），方法本身不做 I/O：
// 只把任务放进进程内有界队列，由固定数量的 worker 查询设备登记、判断前台、渲染文案、
// 加密 payload 并发送。队列满时丢弃（推送是尽力而为，不持久化），绝不阻塞消息投递。
//
// 队列分两条 lane：紧急（私聊新消息、VoIP 来电）与批量（群组/频道扇出、已读/删除同步）。
// 通用 worker 优先取紧急任务；另有 UrgentWorkers 个只取紧急任务的 worker，大群扇出占满
// 通用 worker 时私聊和来电（20 秒内不响铃就没意义）也不排在后面。重试不在 worker 里等待：
// 退避到期后重新放回同一 lane。
//
// 后续扩展点：
//   - 通知规则（静音、预览、静默、other_uids，#96）：NotifyPolicy 与 deliverMessage 里的设备过滤；
//   - 媒体文案（#97）：keys.go 的 messageAlert；
//   - 群组/频道扇出（#98）：NewMessage.Recipients 已是集合，Chat.Kind 选择 payload 字段；
//   - VoIP（#99）、已读/删除同步（#100）：新增事件方法，复用 enqueue 与 send；
//   - 结果分类、重试、失效清理（#101）：classify.go 的 Classify 与 send（所有事件都经 send 发出，
//     发送前核对登记 user_id，token 失效删除登记，可重试失败按 Config.MaxRetries 退避重试）；
//   - 指标（#102）：Metrics；
//   - 相册合并（同一 grouped_id 只推一次）：album.go。
package push

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// DeviceLister 读取用户的设备登记（store.PushDeviceStore 满足它）。
type DeviceLister interface {
	ListPushDevicesByUser(ctx context.Context, userID int64) ([]domain.PushDevice, error)
	// ListPushDevicesByUsers 一次读出多个用户的设备登记（群组/频道推送按页读取，顺序不保证）。
	ListPushDevicesByUsers(ctx context.Context, userIDs []int64) ([]domain.PushDevice, error)
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

// BatchNotifyPolicy 是可一次判定一组收件人的 NotifyPolicy（群组扇出按页使用，SettingsPolicy 实现）。
// 返回的 map 里没有的收件人按零值判定（推送、带预览）。
type BatchNotifyPolicy interface {
	DecideBatch(ctx context.Context, recipientUserIDs []int64, chat Chat) (map[int64]NotifyDecision, error)
}

// Metrics 记录推送计数；SinkMetrics 把它接到 Prometheus 注册表。
type Metrics interface {
	PushQueueDropped(kind string)
	PushSent(kind string, result Result, err error)
	// PushDeviceRemoved 记录一次自动清理的设备登记（reason：invalid_token、user_mismatch）。
	PushDeviceRemoved(reason string)
}

type nopMetrics struct{}

func (nopMetrics) PushQueueDropped(string)        {}
func (nopMetrics) PushSent(string, Result, error) {}
func (nopMetrics) PushDeviceRemoved(string)       {}

// Config 是推送服务的运行参数；零值字段取默认值。
type Config struct {
	// QueueSize 是每条 lane（紧急、批量）待处理任务队列的容量，满了就丢弃。
	QueueSize int
	// Workers 是固定 worker 数。
	Workers int
	// SendTimeout 是单次发送（一台设备）的超时。
	SendTimeout time.Duration
	// MaxRetries 是可重试失败（限流、网关 5xx、超时）之后最多再发几次；0 取默认值，负数不重试。
	MaxRetries int
	// RetryBackoff 是第一次重试前的等待，之后每次翻倍，最长 maxRetryBackoff。
	RetryBackoff time.Duration
	// MemberBatch 是群组/频道推送每批读取的成员数（#98）。
	MemberBatch int
	// UrgentWorkers 是只处理紧急 lane（私聊、来电）的 worker 数；0 取默认值，负数表示没有。
	UrgentWorkers int
	// ShutdownTimeout 是关停时处理队列剩余任务的期限：到期后取消仍在进行的发送，Run 返回。
	ShutdownTimeout time.Duration
	// AlbumDebounce 是相册（同一 grouped_id）合并推送的去抖时间：每来一项重新计时（见 album.go）。
	AlbumDebounce time.Duration
	// AlbumMaxWait 是相册从第一项起最多等多久就推送，即使还在陆续到达。
	AlbumMaxWait time.Duration
}

const (
	defaultQueueSize    = 4096
	defaultWorkers      = 4
	defaultSendTimeout  = 10 * time.Second
	defaultMaxRetries   = 3
	defaultRetryBackoff = 500 * time.Millisecond
	maxRetryBackoff     = 5 * time.Second
	defaultMemberBatch  = 500
	// defaultUrgentWorkers 个专用 worker 足够：紧急任务每条只涉及一个收件人的少数设备。
	defaultUrgentWorkers   = 2
	defaultShutdownTimeout = 5 * time.Second
)

// messageJobKind 是私聊新消息推送任务的类型（lane、重试与指标的 kind）。
const messageJobKind = "message"

// urgentKinds 是走紧急 lane 的任务类型：私聊新消息与 VoIP 来电。其余（群组/频道、同步）走批量 lane。
var urgentKinds = map[string]bool{messageJobKind: true, "voip": true}

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
	// Members 分页列出群组/频道成员（#98）；nil 时 ChannelMessage 只推 NewMessage.Recipients。
	Members MemberPager
}

// NewMessage 是一条新消息事件：Recipients 中每个收件人以 Chat（收件人视角的会话）看到这条消息。
// 私聊每个收件人的 msg_id 不同（各自的 message box），因此私聊每次只带一个收件人。
type NewMessage struct {
	Chat Chat
	// ChatTitle 是群组/频道标题（文案参数）；私聊为空。
	ChatTitle string
	// SenderID 是实际发送者：不给他推送，other_uids 去重也按他判断。
	SenderID int64
	// AuthorName 非空时是文案里展示的发送者名字（匿名管理员、以频道身份发言），
	// 此时 payload 不带 chat_from_id；为空时按 SenderID 读取用户名字。
	AuthorName string
	Recipients []int64
	MessageID  int
	Text       string
	Media      *domain.MessageMedia
	// Silent 是发送方以静默方式发送（message.silent）。
	Silent bool
	// GroupedID 非 0 时是相册的一项：同组各项合并成一条推送（见 album.go）。
	GroupedID int64

	// album 非 nil 时这是合并后的相册（MessageID 等字段取最后一项）。
	album *albumSummary
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
	members  MemberPager
	log      *zap.Logger
	// urgent、bulk 是两条 lane 的有界队列（见包注释）。
	urgent chan job
	bulk   chan job
	// stopping 在 Run 的 ctx 结束后为真：不再安排重试，群组扇出不再翻下一页，相册不再缓冲。
	stopping atomic.Bool
	// albums 是待合并的相册（album.go）。
	albums albumBuffer

	misconfigMu         sync.Mutex
	misconfigLast       time.Time
	misconfigSuppressed int
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
	if cfg.MemberBatch <= 0 {
		cfg.MemberBatch = defaultMemberBatch
	}
	switch {
	case cfg.UrgentWorkers == 0:
		cfg.UrgentWorkers = defaultUrgentWorkers
	case cfg.UrgentWorkers < 0:
		cfg.UrgentWorkers = 0
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.AlbumDebounce <= 0 {
		cfg.AlbumDebounce = defaultAlbumDebounce
	}
	if cfg.AlbumMaxWait <= 0 {
		cfg.AlbumMaxWait = defaultAlbumMaxWait
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
		members:  deps.Members,
		log:      log,
		urgent:   make(chan job, cfg.QueueSize),
		bulk:     make(chan job, cfg.QueueSize),
	}
}

// Enabled 报告服务是否会发推送（配置了发送器与设备存储）。
func (s *Service) Enabled() bool {
	return s != nil && s.sender != nil && s.devices != nil
}

// Run 启动 cfg.Workers 个通用 worker 与 cfg.UrgentWorkers 个紧急 worker，阻塞到 ctx 结束。
// 结束后先把待合并的相册放进队列，处理完队列里已有的任务再返回（不再重试、不再翻页），
// 最多等 cfg.ShutdownTimeout：到期后取消仍在进行的发送并丢下剩余任务。
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		<-ctx.Done()
		return
	}
	s.stopping.Store(false)
	// 任务的 ctx 不随 Run 的 ctx 立即取消：关停时已入队的任务仍能发出，到期限才取消。
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	// worker 看 drainCtx 进入收尾：ctx 结束后先放入待合并的相册，再让 worker 清空队列。
	drainCtx, startDrain := context.WithCancel(context.WithoutCancel(ctx))
	defer startDrain()
	go func() {
		select {
		case <-ctx.Done():
		case <-jobCtx.Done():
			return
		}
		s.stopping.Store(true)
		s.flushAlbums()
		startDrain()
		timer := time.NewTimer(s.cfg.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			s.log.Warn("push shutdown deadline reached; cancelling remaining sends")
			cancelJobs()
		case <-jobCtx.Done():
		}
	}()
	var wg sync.WaitGroup
	for range s.cfg.Workers {
		wg.Go(func() { s.work(drainCtx, jobCtx, true) })
	}
	for range s.cfg.UrgentWorkers {
		wg.Go(func() { s.work(drainCtx, jobCtx, false) })
	}
	wg.Wait()
}

// work 是一个 worker：bulk 为真时两条 lane 都处理（紧急优先），否则只处理紧急 lane。
func (s *Service) work(ctx, jobCtx context.Context, bulk bool) {
	var bulkQueue chan job
	if bulk {
		bulkQueue = s.bulk
	}
	for {
		// 紧急任务优先：先非阻塞地看一眼紧急 lane。
		select {
		case j := <-s.urgent:
			s.runJob(jobCtx, j)
			continue
		default:
		}
		select {
		case j := <-s.urgent:
			s.runJob(jobCtx, j)
		case j := <-bulkQueue:
			s.runJob(jobCtx, j)
		case <-ctx.Done():
			s.drain(jobCtx, bulkQueue)
			return
		}
	}
}

// drain 在关停时处理队列里剩余的任务，直到队列为空或关停期限到。
func (s *Service) drain(jobCtx context.Context, bulkQueue chan job) {
	for jobCtx.Err() == nil {
		select {
		case j := <-s.urgent:
			s.runJob(jobCtx, j)
			continue
		default:
		}
		select {
		case j := <-s.urgent:
			s.runJob(jobCtx, j)
		case j := <-bulkQueue:
			s.runJob(jobCtx, j)
		default:
			return
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

// enqueue 按任务类型非阻塞地放进对应 lane；队列满或服务停用时返回 false。
func (s *Service) enqueue(kind string, run func(ctx context.Context)) bool {
	if !s.Enabled() {
		return false
	}
	queue := s.bulk
	if urgentKinds[kind] {
		queue = s.urgent
	}
	select {
	case queue <- job{kind: kind, run: run}:
		return true
	default:
		s.metrics.PushQueueDropped(kind)
		return false
	}
}

// NewMessage 把一条新消息的推送放进队列，立即返回；返回 false 表示服务停用或队列已满被丢弃。
// 相册的一项（GroupedID 非 0）先放进待合并缓冲区，同组合并成一条推送（album.go）。
func (s *Service) NewMessage(msg NewMessage) bool {
	if !s.Enabled() {
		return false
	}
	msg.Recipients = append([]int64(nil), msg.Recipients...)
	msg.album = nil
	if msg.GroupedID != 0 && s.bufferAlbumItem(false, msg) {
		return true
	}
	return s.enqueueNewMessage(msg)
}

func (s *Service) enqueueNewMessage(msg NewMessage) bool {
	return s.enqueue(messageJobKind, func(ctx context.Context) { s.deliverMessage(ctx, msg) })
}

func (s *Service) deliverMessage(ctx context.Context, msg NewMessage) {
	for _, recipient := range msg.Recipients {
		if recipient == 0 || recipient == msg.SenderID {
			continue
		}
		s.deliverMessageTo(ctx, recipient, msg)
	}
}

// deliverMessageTo 读出私聊收件人的设备登记，过滤（类型、other_uids、前台）、判定通知设置并发送。
func (s *Service) deliverMessageTo(ctx context.Context, recipient int64, msg NewMessage) {
	devices, err := s.devices.ListPushDevicesByUser(ctx, recipient)
	if err != nil {
		s.log.Warn("list push devices failed", zap.Int64("user_id", recipient), zap.Error(err))
		return
	}
	targets := s.messageTargets(recipient, devices, msg)
	if len(targets) == 0 {
		return
	}
	decisions, err := s.decide(ctx, []int64{recipient}, msg.Chat)
	if err != nil {
		s.log.Warn("push notify policy failed", zap.Int64("user_id", recipient), zap.Error(err))
		return
	}
	s.sendMessage(ctx, messageJobKind, newJobRenderer(s.render), recipient, targets, msg, decisions[recipient])
}

// messageTargets 过滤出收件人该收这条消息推送的设备：APNs 普通登记、仍属于收件人、
// 发送者不在 other_uids 里、不在前台。
func (s *Service) messageTargets(recipient int64, devices []domain.PushDevice, msg NewMessage) []domain.PushDevice {
	targets := devices[:0:0]
	for _, device := range devices {
		if device.TokenType != domain.PushTokenAPNs || device.UserID != recipient {
			continue
		}
		// 多账号去重：发送者也登录在这台设备上（other_uids），两个账号都在用户手里，不推。
		if slices.Contains(device.OtherUIDs, msg.SenderID) {
			continue
		}
		if s.presence != nil && s.presence.AuthorizationInForeground(device.AuthKeyID) {
			continue
		}
		targets = append(targets, device)
	}
	return targets
}

// decide 判定一组收件人对会话的通知设置；策略支持批量（BatchNotifyPolicy）时一次判定整批。
// 未注入策略时一律推送、带预览（返回的 map 里没有的收件人即零值判定）。
func (s *Service) decide(ctx context.Context, recipients []int64, chat Chat) (map[int64]NotifyDecision, error) {
	if s.policy == nil || len(recipients) == 0 {
		return nil, nil
	}
	if batch, ok := s.policy.(BatchNotifyPolicy); ok {
		return batch.DecideBatch(ctx, recipients, chat)
	}
	out := make(map[int64]NotifyDecision, len(recipients))
	for _, recipient := range recipients {
		decision, err := s.policy.Decide(ctx, recipient, chat)
		if err != nil {
			return nil, err
		}
		out[recipient] = decision
	}
	return out, nil
}

// sendMessage 按判定结果给收件人的目标设备渲染、加密并发送。render 在一个任务内复用（模板按语言缓存）。
// kind 是任务类型：可重试失败按它回到原来的 lane（群组重试不进紧急 lane），指标也按它计数。
func (s *Service) sendMessage(ctx context.Context, kind string, render *jobRenderer, recipient int64, targets []domain.PushDevice, msg NewMessage, decision NotifyDecision) {
	if decision.Mute {
		return
	}
	senderName := msg.AuthorName
	if senderName == "" {
		senderName = s.displayName(ctx, recipient, msg.SenderID)
	}
	key, args := messageAlert(msg, senderName, msg.ChatTitle, !decision.HidePreview)
	count := noCount
	if msg.album != nil && !decision.HidePreview {
		key, args, count = albumAlert(msg, senderName, msg.ChatTitle)
	}
	silent := msg.Silent || decision.Silent
	for _, device := range targets {
		if len(device.Secret) != 0 && len(device.Secret) != SecretSize {
			// 不能退回明文：那样会把内容暴露给 Apple。
			s.log.Warn("push device secret has unexpected length; skipped",
				zap.Int64("user_id", recipient), zap.String("token", RedactToken(device.Token)), zap.Int("secret_len", len(device.Secret)))
			continue
		}
		content := MessagePush{
			Chat:       msg.Chat,
			FromUserID: msg.chatFromID(),
			MessageID:  msg.MessageID,
			Key:        key,
			Alert:      render.render(ctx, device.LangCode, key, count, args),
			Silent:     silent,
		}
		generic := render.Render(ctx, device.LangCode, KeyEncryptedMessage, "").Body
		payload, err := buildMessagePayload(content, device.Secret, generic)
		if err != nil {
			s.log.Warn("build push payload failed", zap.Int64("user_id", recipient), zap.String("token", RedactToken(device.Token)), zap.Error(err))
			continue
		}
		s.send(ctx, kind, device, Notification{
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
	s.attempt(ctx, kind, device, n, 0, s.cfg.RetryBackoff)
}

// attempt 发第 retry 次（0 是首次）。可重试失败时不在 worker 里等：退避到期后把下一次
// 放回同一 lane 的队列（满了就放弃并计入丢弃），关停期间不再重试。
func (s *Service) attempt(ctx context.Context, kind string, device domain.PushDevice, n Notification, retry int, backoff time.Duration) {
	result, err := s.sendOnce(ctx, kind, n)
	switch Classify(result, err) {
	case OutcomeSent:
		s.log.Debug("push sent", zap.String("kind", kind), zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(n.Token)), zap.String("apns_id", result.ID))
		return
	case OutcomeInvalidToken:
		s.deleteStale(ctx, device, RemovedInvalidToken, "invalid token: "+result.Reason)
		return
	case OutcomeRetryable:
		if retry < s.cfg.MaxRetries && !s.stopping.Load() && ctx.Err() == nil {
			s.log.Debug("push retry", zap.String("kind", kind), zap.Int64("user_id", device.UserID),
				zap.String("token", RedactToken(n.Token)), zap.Int("attempt", retry+1), zap.Int("status", result.StatusCode),
				zap.String("reason", result.Reason), zap.Error(err), zap.Duration("backoff", backoff))
			next := min(backoff*2, maxRetryBackoff)
			time.AfterFunc(backoff, func() {
				if s.stopping.Load() {
					return
				}
				s.enqueue(kind, func(ctx context.Context) { s.attempt(ctx, kind, device, n, retry+1, next) })
			})
			return
		}
	default:
		if err == nil && IsMisconfiguration(result) {
			s.logMisconfiguration(kind, result)
			return
		}
	}
	s.log.Warn("push failed", zap.String("kind", kind), zap.Int64("user_id", device.UserID),
		zap.String("token", RedactToken(n.Token)), zap.Int("status", result.StatusCode),
		zap.String("reason", result.Reason), zap.Int("retries", retry), zap.Error(err))
}

// misconfigLogInterval 是配置错误告警的最小间隔：配错时每条推送都会失败，逐条记 Error 会刷屏。
const misconfigLogInterval = time.Minute

// logMisconfiguration 以 Error 级别记录服务端推送配置错误，每 misconfigLogInterval 最多一条，
// 期间被抑制的条数一并记下。登记保留，修好配置后即恢复推送。
func (s *Service) logMisconfiguration(kind string, result Result) {
	s.misconfigMu.Lock()
	now := time.Now()
	if !s.misconfigLast.IsZero() && now.Sub(s.misconfigLast) < misconfigLogInterval {
		s.misconfigSuppressed++
		s.misconfigMu.Unlock()
		return
	}
	suppressed := s.misconfigSuppressed
	s.misconfigLast, s.misconfigSuppressed = now, 0
	s.misconfigMu.Unlock()
	s.log.Error("apns rejected push: server topic/certificate misconfigured (check TELESRV_APNS_TOPIC and credentials); registrations kept",
		zap.String("kind", kind), zap.Int("status", result.StatusCode), zap.String("reason", result.Reason),
		zap.Int("suppressed_since_last", suppressed))
}

// registrationOwnerMatches 核对登记的 user_id 与授权当前的 user：不一致（或授权已不存在）时
// 删除这条登记并返回 false。读授权失败时这次不发，但保留登记。
// 设备存储随登记读出了授权的 user（AuthorizationUserID）时直接用它，不再逐台查询授权。
func (s *Service) registrationOwnerMatches(ctx context.Context, device domain.PushDevice) bool {
	if device.AuthorizationUserID != 0 {
		if device.AuthorizationUserID == device.UserID {
			return true
		}
		s.deleteStale(ctx, device, RemovedUserMismatch, "authorization user changed")
		return false
	}
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
	s.deleteStale(ctx, device, RemovedUserMismatch, "authorization user changed")
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

// deleteStale 删除判定失效的登记（只删读到的那一版，见 DeviceStore）。reason 是指标标签
// （RemovedInvalidToken、RemovedUserMismatch），确实删掉时才计数。
func (s *Service) deleteStale(ctx context.Context, device domain.PushDevice, reason, why string) {
	deleted, err := s.devices.DeleteStalePushDevice(ctx, device)
	if err != nil {
		s.log.Warn("delete stale push device failed", zap.Int64("user_id", device.UserID),
			zap.String("token", RedactToken(device.Token)), zap.String("why", why), zap.Error(err))
		return
	}
	if deleted {
		s.metrics.PushDeviceRemoved(reason)
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
