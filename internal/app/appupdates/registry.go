// Package appupdates 把客户端更新目录里的安装包（Android APK）注册成 Telegram 文档，供
// help.appUpdate 的 document 下发（docs/update-service.md 的 Android 一节）。
//
// 安卓 Standalone 客户端的普通更新弹窗和侧边栏更新条只认 document：拿到只有 url 的更新时
// 弹窗静默失败，而且客户端会记住这个版本、以后同一版本不再弹出。所以安装包还没入库时
// help.getAppUpdate 一律回答“没有更新”，绝不先给一个没有 document 的结果。
//
// 入库在后台进行：Run 定期列出目录里的安装包逐个注册；Document 遇到还没入库的包时也会
// 触发一次注册。同一个包（按 SHA-256）只入库一次，记在 app_update_documents 里，重启后
// 直接复用。读到的字节按清单记录的大小与 SHA-256 校验，不一致时不入库。
package appupdates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/updatecdn"
)

// Documents 是注册器需要的文件服务能力，*files.Service 满足。
type Documents interface {
	CreateDocumentFromReader(ctx context.Context, r io.Reader, spec domain.DocumentSpec, wantSize int64) (domain.Document, error)
	GetDocument(ctx context.Context, id int64) (domain.Document, bool, error)
}

const (
	defaultScanInterval  = 30 * time.Second
	defaultRetryInterval = time.Minute
	// 一次注册（读完整个安装包并写进 blob 存储）的时限。
	defaultRegisterTimeout = 30 * time.Minute
)

// Registry 注册并缓存安装包对应的文档。
type Registry struct {
	source updatecdn.AppFileSource
	docs   store.AppUpdateDocumentStore
	files  Documents
	log    *zap.Logger
	now    func() time.Time

	scanInterval    time.Duration
	retryInterval   time.Duration
	registerTimeout time.Duration

	mu       sync.Mutex
	ready    map[string]domain.Document // sha256 → 已入库的文档
	inflight map[string]bool
	failedAt map[string]time.Time
	// wg 跟踪后台注册，测试用 Wait 等它们结束。
	wg sync.WaitGroup
}

// Option 调整注册器。
type Option func(*Registry)

// WithClock 注入时钟（失败重试的间隔判断），默认 time.Now。
func WithClock(now func() time.Time) Option {
	return func(r *Registry) {
		if now != nil {
			r.now = now
		}
	}
}

// WithIntervals 调整扫描间隔与失败后的重试间隔（测试用）；非正值保留默认。
func WithIntervals(scan, retry time.Duration) Option {
	return func(r *Registry) {
		if scan > 0 {
			r.scanInterval = scan
		}
		if retry > 0 {
			r.retryInterval = retry
		}
	}
}

func NewRegistry(source updatecdn.AppFileSource, docs store.AppUpdateDocumentStore, files Documents, log *zap.Logger, opts ...Option) *Registry {
	if log == nil {
		log = zap.NewNop()
	}
	r := &Registry{
		source: source, docs: docs, files: files, log: log, now: time.Now,
		scanInterval: defaultScanInterval, retryInterval: defaultRetryInterval, registerTimeout: defaultRegisterTimeout,
		ready: make(map[string]domain.Document), inflight: make(map[string]bool), failedAt: make(map[string]time.Time),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Document 返回安装包已入库的文档；还没入库时返回 ready=false，并在后台开始注册（失败后
// 在重试间隔内不再重复触发）。只有读存储出错时返回 error。
func (r *Registry) Document(ctx context.Context, file updatecdn.ResolvedFile) (domain.Document, bool, error) {
	if r == nil {
		return domain.Document{}, false, nil
	}
	doc, ok, err := r.lookup(ctx, file)
	if err != nil || ok {
		return doc, ok, err
	}
	r.start(file)
	return domain.Document{}, false, nil
}

// Ready 只报告安装包是否已入库，不触发注册（管理后台显示用）。
func (r *Registry) Ready(ctx context.Context, sha256 string) (bool, error) {
	if r == nil {
		return false, nil
	}
	_, ok, err := r.lookup(ctx, updatecdn.ResolvedFile{SHA256: sha256})
	return ok, err
}

// lookup 先查内存缓存，再查 app_update_documents 与文档本身。
func (r *Registry) lookup(ctx context.Context, file updatecdn.ResolvedFile) (domain.Document, bool, error) {
	sha := strings.ToLower(file.SHA256)
	r.mu.Lock()
	doc, ok := r.ready[sha]
	r.mu.Unlock()
	if ok {
		return doc, true, nil
	}
	record, found, err := r.docs.AppUpdateDocument(ctx, sha)
	if err != nil || !found {
		return domain.Document{}, false, err
	}
	doc, found, err = r.files.GetDocument(ctx, record.DocumentID)
	if err != nil || !found {
		return domain.Document{}, false, err
	}
	r.mu.Lock()
	r.ready[sha] = doc
	r.mu.Unlock()
	return doc, true, nil
}

// start 在后台注册 file，同一个包同时只注册一次，失败后在重试间隔内不再触发。
func (r *Registry) start(file updatecdn.ResolvedFile) {
	sha := strings.ToLower(file.SHA256)
	r.mu.Lock()
	if r.inflight[sha] || (r.failedAt[sha] != time.Time{} && r.now().Sub(r.failedAt[sha]) < r.retryInterval) {
		r.mu.Unlock()
		return
	}
	r.inflight[sha] = true
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), r.registerTimeout)
		defer cancel()
		_, _ = r.register(ctx, file)
	}()
}

// Ensure 同步注册 file（已入库时直接返回文档）。并发调用同一个包时只有一个真正注册，其余
// 返回 ErrRegistrationInProgress。
func (r *Registry) Ensure(ctx context.Context, file updatecdn.ResolvedFile) (domain.Document, error) {
	if doc, ok, err := r.lookup(ctx, file); err != nil || ok {
		return doc, err
	}
	sha := strings.ToLower(file.SHA256)
	r.mu.Lock()
	if r.inflight[sha] {
		r.mu.Unlock()
		return domain.Document{}, ErrRegistrationInProgress
	}
	r.inflight[sha] = true
	r.mu.Unlock()
	return r.register(ctx, file)
}

// ErrRegistrationInProgress 表示同一个安装包正在注册。
var ErrRegistrationInProgress = errors.New("installer package registration in progress")

// register 读取安装包并入库，调用前必须已把 sha 标记为 inflight。
func (r *Registry) register(ctx context.Context, file updatecdn.ResolvedFile) (domain.Document, error) {
	sha := strings.ToLower(file.SHA256)
	started := r.now()
	doc, err := r.registerOnce(ctx, file)
	r.mu.Lock()
	delete(r.inflight, sha)
	if err != nil {
		r.failedAt[sha] = r.now()
	} else {
		delete(r.failedAt, sha)
		r.ready[sha] = doc
	}
	r.mu.Unlock()
	if err != nil {
		r.log.Warn("registering an update installer package as a document failed; retrying later",
			zap.String("file", file.Name), zap.String("sha256", sha), zap.Error(err))
		return domain.Document{}, err
	}
	r.log.Info("update installer package registered as a document",
		zap.String("file", file.Name), zap.String("sha256", sha), zap.Int64("size", file.Size),
		zap.Int64("document_id", doc.ID), zap.Duration("took", r.now().Sub(started)))
	return doc, nil
}

func (r *Registry) registerOnce(ctx context.Context, file updatecdn.ResolvedFile) (domain.Document, error) {
	if !file.Valid() {
		return domain.Document{}, fmt.Errorf("invalid installer package metadata %q", file.Name)
	}
	// 另一个实例（或上一次运行）可能刚刚入库。
	if doc, ok, err := r.lookup(ctx, file); err != nil || ok {
		return doc, err
	}
	body, err := r.source.OpenAppFile(ctx, file)
	if err != nil {
		return domain.Document{}, err
	}
	defer body.Close()
	created, err := r.files.CreateDocumentFromReader(ctx, body, domain.DocumentSpec{
		MimeType:   file.MimeType,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: file.Name}},
		ForceFile:  true,
	}, file.Size)
	if err != nil {
		return domain.Document{}, fmt.Errorf("store installer package %q: %w", file.Name, err)
	}
	sha := strings.ToLower(file.SHA256)
	if err := r.docs.PutAppUpdateDocument(ctx, domain.AppUpdateDocument{SHA256: sha, DocumentID: created.ID, FileName: file.Name, Size: file.Size}); err != nil {
		return domain.Document{}, err
	}
	// 并发入库时以表里先写进去的那一行为准。
	doc, ok, err := r.lookup(ctx, file)
	if err != nil {
		return domain.Document{}, err
	}
	if !ok {
		return domain.Document{}, fmt.Errorf("installer package %q: document %d vanished after registration", file.Name, created.ID)
	}
	return doc, nil
}

// Scan 列出目录里的安装包并逐个注册还没入库的那些（同步，一次一个）。
func (r *Registry) Scan(ctx context.Context) error {
	files, err := r.source.AppFiles(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, ok, err := r.lookup(ctx, file); err != nil || ok {
			if err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		sha := strings.ToLower(file.SHA256)
		r.mu.Lock()
		skip := r.inflight[sha] || (r.failedAt[sha] != time.Time{} && r.now().Sub(r.failedAt[sha]) < r.retryInterval)
		if !skip {
			r.inflight[sha] = true
		}
		r.mu.Unlock()
		if skip {
			continue
		}
		regCtx, cancel := context.WithTimeout(ctx, r.registerTimeout)
		if _, err := r.register(regCtx, file); err != nil && firstErr == nil {
			firstErr = err
		}
		cancel()
	}
	return firstErr
}

// Run 立即扫描一次，之后每个扫描间隔扫描一次，直到 ctx 结束。扫描失败只记日志。
func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(r.scanInterval)
	defer ticker.Stop()
	for {
		if err := r.Scan(ctx); err != nil && ctx.Err() == nil {
			r.log.Debug("update installer package scan", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			r.wg.Wait()
			return
		case <-ticker.C:
		}
	}
}

// Wait 等待后台触发的注册结束（测试用）。
func (r *Registry) Wait() { r.wg.Wait() }
