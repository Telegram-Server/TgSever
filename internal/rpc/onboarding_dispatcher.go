package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/app/onboarding"
	"telesrv/internal/domain"
)

// 新账号默认入驻的调度器：领取建号时写入的入驻任务，交给 app/onboarding 执行（加默认联系人、
// 直接加入默认的群和频道），再把落下的变化推送给在线客户端，推送方式与用户自己加联系人、
// 加入群时相同。

const (
	defaultOnboardingBatch    = 50
	defaultOnboardingInterval = 500 * time.Millisecond
	defaultOnboardingLease    = 2 * time.Minute
)

// OnboardingService 执行入驻任务（生产里是 app/onboarding.Service）。
type OnboardingService interface {
	ProcessPending(ctx context.Context, limit int, lease time.Duration, publish onboarding.Publisher) (int, error)
}

// OnboardingDispatcher 周期性地执行到期的入驻任务。
type OnboardingDispatcher struct {
	router          *Router
	log             *zap.Logger
	batch           int
	interval        time.Duration
	maxIdleInterval time.Duration
	lease           time.Duration
}

// NewOnboardingDispatcher 创建入驻调度器。
func NewOnboardingDispatcher(router *Router, log *zap.Logger) *OnboardingDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	return &OnboardingDispatcher{
		router:          router,
		log:             log,
		batch:           defaultOnboardingBatch,
		interval:        defaultOnboardingInterval,
		maxIdleInterval: defaultIdleDispatchMaxInterval,
		lease:           defaultOnboardingLease,
	}
}

// Run 阻塞运行调度循环直到 ctx 取消；空闲时逐步拉长间隔。
func (d *OnboardingDispatcher) Run(ctx context.Context) {
	if d == nil || d.router == nil || d.router.deps.Onboarding == nil {
		return
	}
	runIdleBackoffLoop(ctx, d.interval, d.maxIdleInterval, d.DispatchOnce)
}

// DispatchOnce 执行一批到期的任务，返回是否领取到了任务。
func (d *OnboardingDispatcher) DispatchOnce(ctx context.Context) bool {
	if d == nil || d.router == nil || d.router.deps.Onboarding == nil {
		return false
	}
	n, err := d.router.deps.Onboarding.ProcessPending(ctx, d.batch, d.lease, d.publish)
	if err != nil {
		d.log.Warn("process onboarding jobs", zap.Error(err))
	}
	return n > 0
}

func (d *OnboardingDispatcher) publish(ctx context.Context, job domain.OnboardingJob, res onboarding.Result) {
	for _, s := range res.Skipped {
		d.log.Info("onboarding item skipped",
			zap.Int64("user_id", job.UserID),
			zap.String("kind", string(s.Kind)),
			zap.Int64("id", s.ID),
			zap.String("reason", s.Reason),
		)
	}
	d.router.publishOnboardingResult(ctx, job, res, d.log)
}

// publishOnboardingResult 推送一次入驻落下的变化：每条写入的联系人让通讯录主人记一条
// peer settings 与 contacts reset 并推送；每次入群照 channels.joinChannel 的方式扇出。
func (r *Router) publishOnboardingResult(ctx context.Context, job domain.OnboardingJob, res onboarding.Result, log *zap.Logger) {
	for _, c := range res.Contacts {
		r.invalidateRPCProjectionForViewer(c.OwnerUserID)
		if err := r.recordAcceptedContactTargetUpdates(ctx, c.Contact.User.ID, c.OwnerUserID); err != nil {
			log.Warn("publish onboarding contact", zap.Int64("owner_user_id", c.OwnerUserID), zap.Int64("contact_user_id", c.Contact.User.ID), zap.Error(err))
		}
	}
	for _, joined := range res.Joins {
		r.invalidateChannelFullBotInfoCacheForChannel(joined.Channel.ID)
		r.addOnlineChannelMemberships(joined.Channel.ID, channelMemberUserIDs(joined.Members)...)
		r.pushChannelUpdates(ctx, job.UserID, joined.Channel.ID, joined.Recipients, func(viewerUserID int64) *tg.Updates {
			return r.channelOperationUpdates(ctx, viewerUserID, joined)
		})
		r.pushBotUpdates(ctx, joined.BotUpdates)
	}
}
