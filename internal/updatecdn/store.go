package updatecdn

import (
	"context"
	"crypto/sha256"
	"os"
	"sync"
	"time"
)

// racyWindow 覆盖粗粒度文件系统的 mtime 精度（FAT 为 2 秒，且向上取整，mtime 可能
// 略晚于写入时刻）。清单的 mtime 与读取时刻相差不足这个窗口时，同一时间戳内、同样
// 大小的改写无法从 stat 看出来，只能比较内容。
const racyWindow = 2 * time.Second

// defaultReloadRetryInterval 是失败的加载在清单未变时的重试间隔。失败原因可能在
// 清单之外（包还没上传完、签名尚未生效），所以不能只等清单变化；但也不能每个请求
// 都重新读盘验签。
const defaultReloadRetryInterval = 30 * time.Second

// Store serves the last successfully loaded catalog and reloads it when the
// manifest changes. A broken new manifest is never mixed with, and never
// replaces, the previous good snapshot.
//
// 变化检测：平时每次请求只做一次 stat，比较 mtime 与大小；清单的 mtime 与读取时刻
// 相差不足 racyWindow 时（刚被改写过），额外读取内容比较 SHA-256，直到确认内容未变且
// 时间戳已足够旧为止——此后任何改写都必然带来新的 mtime。
//
// 重新加载失败时继续提供上一份正常快照，Err 返回失败原因，并调用
// WithReloadErrorHandler 注册的回调。清单未变时失败的加载最多每 30 秒重试一次，
// 不会每个请求都重新读盘验签；首次加载失败则由 NewStore 直接返回错误。
//
// 加载在 Store 的锁内进行，期间其他请求等待（与此前行为一致）。
type Store struct {
	manifestPath  string
	filesDir      string
	opts          CatalogOptions
	onReloadError func(error)
	retryInterval time.Duration

	mu      sync.RWMutex
	catalog *Catalog // 最近一份加载成功的快照
	// 最近一次加载尝试（无论成败）时清单的 stat 结果与内容摘要。statOK 为 false
	// 表示当时 stat 就失败了（例如清单被删除）。
	statOK    bool
	modTime   time.Time
	size      int64
	digest    [sha256.Size]byte
	racy      bool
	attemptAt time.Time
	err       error // 最近一次加载失败的原因；加载成功后清空
}

// StoreOption 调整 Store 的可选行为。
type StoreOption func(*Store)

// WithReloadErrorHandler 注册重新加载失败时的回调（例如记录日志）。每次失败的加载
// 尝试回调一次；清单未变时失败的加载最多每 30 秒重试一次，因此它不会随请求量刷屏。
// 回调在 Store 的锁之外执行。首次加载失败不回调，由 NewStore 返回错误。
func WithReloadErrorHandler(fn func(error)) StoreOption {
	return func(s *Store) { s.onReloadError = fn }
}

// NewStore loads the catalog once and reloads it with the same opts (including
// the update root public key) whenever the manifest changes.
func NewStore(manifestPath, filesDir string, opts CatalogOptions, storeOpts ...StoreOption) (*Store, error) {
	store := &Store{manifestPath: manifestPath, filesDir: filesDir, opts: opts, retryInterval: defaultReloadRetryInterval}
	for _, opt := range storeOpts {
		opt(store)
	}
	if _, err := store.Snapshot(); err != nil {
		return nil, err
	}
	return store, nil
}

// Snapshot returns the current catalog, reloading it first when the manifest
// changed. After the first successful load it never fails: a broken reload
// keeps serving the previous snapshot (see Err).
func (s *Store) Snapshot() (*Catalog, error) {
	catalog, _, err := s.snapshot()
	return catalog, err
}

// snapshot 返回快照、此刻的重新加载错误 stale（快照已过期的原因，与快照在同一把锁
// 下取得）以及无快照可用时的错误 err。
func (s *Store) snapshot() (catalog *Catalog, stale, err error) {
	info, statErr := os.Stat(s.manifestPath)
	now := time.Now()
	s.mu.RLock()
	if s.catalog != nil && !s.racy && s.currentLocked(info, statErr, now) {
		catalog, stale = s.catalog, s.err
		s.mu.RUnlock()
		return catalog, stale, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	info, statErr = os.Stat(s.manifestPath)
	now = time.Now()
	if s.catalog != nil && s.currentLocked(info, statErr, now) && !s.contentChangedLocked(now) {
		catalog, stale = s.catalog, s.err
		s.mu.Unlock()
		return catalog, stale, nil
	}
	reloadErr := s.reloadLocked(info, statErr, now)
	catalog = s.catalog
	s.mu.Unlock()

	if catalog == nil {
		return nil, nil, reloadErr
	}
	if reloadErr != nil && s.onReloadError != nil {
		s.onReloadError(reloadErr)
	}
	return catalog, reloadErr, nil
}

// Err reports why the latest manifest could not be loaded while an older
// snapshot is being served; nil when the served catalog is current.
func (s *Store) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// Resolve answers help.getAppUpdate in process from the current snapshot. It
// implements Resolver, like the HTTP Client for a separate telesrv-update.
func (s *Store) Resolve(_ context.Context, req ResolveRequest) (*ResolvedUpdate, error) {
	catalog, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	return catalog.Resolve(req)
}

// currentLocked 判断上次加载尝试的结果是否仍然有效：stat 与上次一致，且上次没有
// 失败或失败后还没到重试时间。stat 失败只与“上次也 stat 失败”一致，这样被删掉的
// 清单同样按重试间隔报告。
func (s *Store) currentLocked(info os.FileInfo, statErr error, now time.Time) bool {
	if s.err != nil && now.Sub(s.attemptAt) >= s.retryInterval {
		return false
	}
	if statErr != nil {
		return !s.statOK
	}
	return s.statOK && info.ModTime().Equal(s.modTime) && info.Size() == s.size
}

// contentChangedLocked 只在 racy 时读盘比较内容；内容未变且 mtime 已早于 racyWindow
// 时解除 racy，回到只比较 stat。
func (s *Store) contentChangedLocked(now time.Time) bool {
	if !s.racy {
		return false
	}
	// 只比较字节：同样的字节解码与校验结果相同，解码错误不需要单独区分；读不出内容时
	// 摘要为零值，与上次同样读不出时一致。
	_, _, digest, _ := readManifestFileDigest(s.manifestPath)
	if digest != s.digest {
		return true
	}
	s.racy = isRacy(s.modTime, now)
	return false
}

// reloadLocked 加载清单；成功时替换快照，失败时保留旧快照并记下这次尝试。stat 结果
// 取自加载前的 os.Stat：加载期间清单若又被改写，下一次请求看到新的 stat 会再加载。
func (s *Store) reloadLocked(info os.FileInfo, statErr error, now time.Time) error {
	s.attemptAt = now
	s.statOK = statErr == nil
	s.racy = false
	s.digest = [sha256.Size]byte{}
	if statErr == nil {
		s.modTime, s.size = info.ModTime(), info.Size()
	}
	manifest, _, digest, err := readManifestFileDigest(s.manifestPath)
	if statErr == nil {
		s.digest = digest
		s.racy = isRacy(s.modTime, now)
	}
	var catalog *Catalog
	if err == nil {
		catalog, err = newCatalog(manifest, s.filesDir, s.opts, nil)
	}
	if err != nil {
		s.err = err
		return err
	}
	s.catalog, s.err = catalog, nil
	return nil
}

// isRacy 判断 mtime 是否落在读取时刻前后 racyWindow 之内。远在未来的 mtime（时钟
// 偏差）不算：否则会永远停在逐请求比较内容的慢路径上。
func isRacy(modTime, now time.Time) bool {
	d := now.Sub(modTime)
	return d > -racyWindow && d < racyWindow
}
