package rpc

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Bot API enqueue 异步化：候选查询、bot 判定与 bot_api_updates 写入都在
// 单 worker FIFO 内执行，避免发送者/反应 RPC 等待额外的存储往返。
//
// 队列行本身是投递真值，已接收的任务不能丢弃，满时也不能旁路插入旧任务之前。
// 满时背压入队并等待执行；请求取消可退出等待，停止时 drain 已接收任务。

const (
	defaultBotAPIEnqueueBuffer  = 4096
	botAPIEnqueueJobTimeout     = 10 * time.Second
	botAPIEnqueuePressureReason = "bot api enqueue queue full, waiting for ordered insert"
)

type botAPIEnqueueJob struct {
	run  func(context.Context)
	done chan struct{}
}

type botAPIEnqueueDispatcher struct {
	log      *zap.Logger
	jobs     chan botAPIEnqueueJob
	started  atomic.Bool
	admit    chan struct{}
	stopping chan struct{}
	done     chan struct{}
}

func newBotAPIEnqueueDispatcher(log *zap.Logger, buffer int) *botAPIEnqueueDispatcher {
	if buffer <= 0 {
		buffer = defaultBotAPIEnqueueBuffer
	}
	return &botAPIEnqueueDispatcher{
		log:      log.Named("botapi-enqueue"),
		jobs:     make(chan botAPIEnqueueJob, buffer),
		admit:    make(chan struct{}, 1),
		stopping: make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// RunBotAPIEnqueue 启动 Bot API enqueue 后台 worker，由 main 与其它 dispatcher 一同 go 起。
// 阻塞到 ctx 取消；未调用前 enqueue 同步执行（行为同旧版，测试/未装配场景不变）。
func (r *Router) RunBotAPIEnqueue(ctx context.Context) {
	r.botAPIEnqueueQueue.Run(ctx)
}

func (d *botAPIEnqueueDispatcher) Run(ctx context.Context) {
	if d == nil || !d.started.CompareAndSwap(false, true) {
		return
	}
	defer close(d.done)
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case job := <-d.jobs:
			d.execute(ctx, job)
		}
	}
	// Wake admissions blocked by a full queue before taking the admission
	// barrier. Everything accepted before the barrier drains with a fresh ctx.
	close(d.stopping)
	d.admit <- struct{}{}
	<-d.admit
	for {
		select {
		case job := <-d.jobs:
			d.execute(ctx, job)
		default:
			return
		}
	}
}

func (d *botAPIEnqueueDispatcher) execute(ctx context.Context, job botAPIEnqueueJob) {
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), botAPIEnqueueJobTimeout)
	defer cancel()
	if job.done != nil {
		defer close(job.done)
	}
	job.run(jobCtx)
}

// Enqueue 未启动时同步执行；正常时非阻塞投入 FIFO。队列满时保留背压/
// 同步等待语义，但仍由单 worker 按顺序执行，绝不越过队列里的旧事件。
// 停止后等待 drain 完毕再同步执行，避免任务留在无人消费的队列。
func (d *botAPIEnqueueDispatcher) Enqueue(reqCtx context.Context, run func(context.Context)) {
	if d == nil || run == nil {
		return
	}
	if !d.started.Load() {
		run(reqCtx)
		return
	}
	select {
	case d.admit <- struct{}{}:
	case <-reqCtx.Done():
		d.log.Warn("bot api enqueue canceled before admission", zap.Error(reqCtx.Err()))
		return
	}
	select {
	case <-d.stopping:
		<-d.admit
		d.afterDrain(reqCtx, run)
		return
	default:
	}
	job := botAPIEnqueueJob{run: run}
	select {
	case d.jobs <- job:
		<-d.admit
		return
	default:
		d.log.Warn(botAPIEnqueuePressureReason)
	}
	job.done = make(chan struct{})
	select {
	case d.jobs <- job:
		<-d.admit
		select {
		case <-job.done:
		case <-reqCtx.Done():
			// Already accepted: the worker still delivers the immutable job.
		}
	case <-reqCtx.Done():
		<-d.admit
		d.log.Warn("bot api enqueue canceled before admission", zap.Error(reqCtx.Err()))
	case <-d.stopping:
		<-d.admit
		d.afterDrain(reqCtx, run)
	}
}

func (d *botAPIEnqueueDispatcher) afterDrain(reqCtx context.Context, run func(context.Context)) {
	select {
	case <-d.done:
		run(reqCtx)
	case <-reqCtx.Done():
		d.log.Warn("bot api enqueue canceled before admission", zap.Error(reqCtx.Err()))
	}
}
