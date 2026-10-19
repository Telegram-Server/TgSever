package updatecdn

import (
	"context"
	"crypto/sha256"
	"os"
	"sync"
	"time"
)

// racyWindow 覆盖粗粒度文件系统的 mtime 精度（FAT 为 2 秒）。清单的 mtime 距离读取
// 时刻不足这个窗口时，同一时间戳内、同样大小的改写无法从 stat 看出来，只能比较内容。
const racyWindow = 2 * time.Second

// Store serves the last successfully loaded catalog and reloads it when the
// manifest changes. A broken new manifest is never mixed with, and never
// replaces, the previous good snapshot.
//
// 变化检测：平时每次请求只做一次 stat，比较 mtime 与大小；清单的 mtime 距离读取时刻
// 不足 racyWindow 时（刚被改写过），额外读取内容比较 SHA-256，直到确认内容未变且
// 时间戳已足够旧为止——此后任何改写都必然带来新的 mtime。
//
// 重新加载失败时继续提供上一份正常快照，Err 返回失败原因，并调用
// WithReloadErrorHandler 注册的回调。同一份坏清单（stat 与内容都未变）只尝试加载
// 一次，不会每个请求都重新读盘验签；首次加载失败则由 NewStore 直接返回错误。
type Store struct {
	manifestPath  string
	filesDir      string
	opts          CatalogOptions
	onReloadError func(error)

	mu      sync.RWMutex
	catalog *Catalog // 最近一份加载成功的快照
	// 最近一次尝试加载（无论成败）的清单：statOK 为 false 表示当时连打开都失败了。
	statOK  bool
	modTime time.Time
	size    int64
	digest  [sha256.Size]byte
	racy    bool
	err     error // 最近一次加载失败的原因；加载成功后清空
}

// StoreOption 调整 Store 的可选行为。
type StoreOption func(*Store)

// WithReloadErrorHandler 注册重新加载失败时的回调（例如记录日志）。每次失败的加载
// 尝试回调一次；由于同一份坏清单不会被重复加载，它不会随请求量刷屏。回调在 Store
// 的锁之外执行。首次加载失败不回调，由 NewStore 返回错误。
func WithReloadErrorHandler(fn func(error)) StoreOption {
	return func(s *Store) { s.onReloadError = fn }
}

// NewStore loads the catalog once and reloads it with the same opts (including
// the update root public key) whenever the manifest changes.
func NewStore(manifestPath, filesDir string, opts CatalogOptions, storeOpts ...StoreOption) (*Store, error) {
	store := &Store{manifestPath: manifestPath, filesDir: filesDir, opts: opts}
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
	info, statErr := os.Stat(s.manifestPath)
	s.mu.RLock()
	if s.catalog != nil && !s.racy && s.sameStatLocked(info, statErr) {
		catalog := s.catalog
		s.mu.RUnlock()
		return catalog, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	info, statErr = os.Stat(s.manifestPath)
	if s.catalog != nil && s.sameStatLocked(info, statErr) && !s.contentChangedLocked() {
		catalog := s.catalog
		s.mu.Unlock()
		return catalog, nil
	}
	reloadErr := s.reloadLocked()
	catalog, previous := s.catalog, s.catalog != nil && reloadErr != nil
	s.mu.Unlock()

	if catalog == nil {
		return nil, reloadErr
	}
	if previous && s.onReloadError != nil {
		s.onReloadError(reloadErr)
	}
	return catalog, nil
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

// sameStatLocked 判断 stat 结果是否与最近一次尝试加载的清单一致。stat 失败只与
// “上次也没能打开”一致，这样被删掉的清单同样只报告一次。
func (s *Store) sameStatLocked(info os.FileInfo, statErr error) bool {
	if statErr != nil {
		return !s.statOK && s.err != nil
	}
	return s.statOK && info.ModTime().Equal(s.modTime) && info.Size() == s.size
}

// contentChangedLocked 只在 racy 时读盘比较内容；内容未变且 mtime 已早于 racyWindow
// 时解除 racy，回到只比较 stat。
func (s *Store) contentChangedLocked() bool {
	if !s.racy {
		return false
	}
	readAt := time.Now()
	// 只比较字节：同样的字节解码与校验结果相同，解码错误不需要单独区分。
	_, info, digest, _ := readManifestFileDigest(s.manifestPath)
	if info == nil || digest != s.digest {
		return true
	}
	if readAt.Sub(info.ModTime()) >= racyWindow {
		s.racy = false
	}
	return false
}

// reloadLocked 加载清单；成功时替换快照，失败时保留旧快照并记下这份坏清单。
func (s *Store) reloadLocked() error {
	readAt := time.Now()
	manifest, info, digest, err := readManifestFileDigest(s.manifestPath)
	var catalog *Catalog
	if err == nil {
		catalog, err = newCatalog(manifest, s.filesDir, s.opts, nil)
	}
	s.statOK = info != nil
	if info != nil {
		s.modTime, s.size, s.digest = info.ModTime(), info.Size(), digest
		s.racy = readAt.Sub(info.ModTime()) < racyWindow
	} else {
		s.racy = false
	}
	if err != nil {
		s.err = err
		return err
	}
	s.catalog, s.err = catalog, nil
	return nil
}
