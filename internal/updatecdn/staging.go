package updatecdn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 暂存区：管理后台上传的包先流式写进清单所在目录下的 .staging，预检（DryRun）与确认
// 发布都在同一个暂存文件上运行，确认时只带暂存 id，不必重新上传。暂存目录与清单、
// 文件目录在同一个更新数据卷上；名称以 . 开头，且 Handler 只提供清单引用的文件目录
// 中的文件，所以暂存的包永远不会被下发。
//
// 暂存文件名就是暂存 id（16 字节随机数的十六进制），写入中的文件带 .part 后缀；查找
// 时 id 必须恰好是 32 个小写十六进制字符，任何路径都拼不出来。暂存按修改时间过期
// （默认 DefaultStagingTTL），每次暂存与发布时顺带清理过期的暂存与中断上传留下的
// .part 文件；发布成功后删除对应的暂存文件。确认发布时先把暂存改名为 <id>.claim
// 认领，同一个 id 的两次确认只有一次能发布。同时暂存的数量与总字节有上限
// （stagingMaxPending、stagingMaxPendingBytes），反复上传占不满更新数据卷。

// DefaultStagingTTL 是暂存包的默认有效期：过期后要重新上传，不能用很久以前预检的
// 结果去发布。
const DefaultStagingTTL = 30 * time.Minute

const (
	stagingDirName    = ".staging"
	stagingPartSuffix = ".part"
	// stagingClaimSuffix 标记正在发布的暂存（见 PublishStagedPackage）。
	stagingClaimSuffix = ".claim"
	stagingIDBytes     = 16
)

var (
	// ErrStagingNotFound 表示暂存 id 不存在（从未暂存、已发布、已放弃或已被清理），
	// 或者根本不是一个合法的暂存 id。
	ErrStagingNotFound = errors.New("staged package not found")
	// ErrStagingExpired 表示暂存包已超过有效期；它在报告时被删除。
	ErrStagingExpired = errors.New("staged package expired")
	// ErrStagedPackageTooLarge 表示上传超过了 v2 包的大小上限，传输被中止。
	ErrStagedPackageTooLarge = fmt.Errorf("package exceeds the v2 package limit of %d bytes", int64(MaxDesktopPackageSize))
	// ErrStagingFull 表示同时暂存（含正在上传）的包已达数量或总字节上限；等暂存被
	// 发布或过期后再上传。
	ErrStagingFull = errors.New("too many pending uploads; publish or wait for a staged package to expire")
	// ErrStagingReadFailed 表示读取上传内容失败：客户端断开、读取超时等，属于客户端
	// 一侧的问题，重新上传即可。
	ErrStagingReadFailed = errors.New("reading the upload failed")
	// ErrStagingStorage 表示本机写暂存失败：建目录、生成 id、写盘（磁盘满）等，属于
	// 服务器一侧的问题。
	ErrStagingStorage = errors.New("staging storage failed")
)

// 暂存的上限，测试中调小：单个包的大小，以及同时暂存（含 .part）的数量与总字节。
// 每个运营者一次上传一个包，几个名额足够；总字节上限防止反复上传占满更新数据卷。
var (
	stagingSizeLimit       int64 = MaxDesktopPackageSize
	stagingMaxPending            = 4
	stagingMaxPendingBytes int64 = 4 * MaxDesktopPackageSize
)

// StagedPackage 是一个暂存成功的包。
type StagedPackage struct {
	ID     string
	Size   int64
	SHA256 string
}

// StagingDir returns the staging directory for manifestPath: ".staging" next
// to the manifest, on the same volume as the manifest and the files directory.
func StagingDir(manifestPath string) string {
	return filepath.Join(filepath.Dir(manifestPath), stagingDirName)
}

// StagePackage streams r into a new file in the staging directory and returns
// its unguessable id, size and SHA-256. At most MaxDesktopPackageSize bytes
// are accepted: the transfer stops one byte past the limit and the partial
// file is removed, as it is on a read error. Expired stagings are cleaned
// first. The staged file's modification time is set to now, from which its
// ttl runs.
//
// Failures are classified: ErrStagedPackageTooLarge, ErrStagingFull when
// stagingMaxPending stagings or stagingMaxPendingBytes are already pending
// (checked after the cleanup, before anything is read), ErrStagingReadFailed
// when reading r fails (the client's side) and ErrStagingStorage when the
// staging directory cannot be written (the server's side).
func StagePackage(manifestPath string, r io.Reader, now time.Time, ttl time.Duration) (StagedPackage, error) {
	dir := StagingDir(manifestPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return StagedPackage{}, stagingStorageError("create staging directory", err)
	}
	_, _ = CleanupStagedPackages(manifestPath, now, ttl)
	if err := checkStagingCapacity(dir); err != nil {
		return StagedPackage{}, err
	}

	var raw [stagingIDBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return StagedPackage{}, stagingStorageError("generate staging id", err)
	}
	id := hex.EncodeToString(raw[:])
	final := filepath.Join(dir, id)
	part := final + stagingPartSuffix
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return StagedPackage{}, stagingStorageError("create staging file", err)
	}
	placed := false
	defer func() {
		if !placed {
			_ = os.Remove(part)
		}
	}()

	hash := sha256.New()
	limit := stagingSizeLimit
	body := &readErrorReader{r: io.LimitReader(r, limit+1)}
	written, err := io.Copy(io.MultiWriter(f, hash), body)
	switch {
	case body.err != nil:
		err = fmt.Errorf("%w: %w", ErrStagingReadFailed, body.err)
	case err != nil:
		err = stagingStorageError("write staging file", err)
	case written > limit:
		err = ErrStagedPackageTooLarge
	default:
		if err = f.Sync(); err != nil {
			err = stagingStorageError("sync staging file", err)
		}
	}
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = stagingStorageError("close staging file", closeErr)
	}
	if err != nil {
		return StagedPackage{}, err
	}
	if err := os.Rename(part, final); err != nil {
		return StagedPackage{}, stagingStorageError("place staging file", err)
	}
	placed = true
	if err := os.Chtimes(final, now, now); err != nil {
		_ = os.Remove(final)
		return StagedPackage{}, stagingStorageError("stamp staging file", err)
	}
	return StagedPackage{ID: id, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

// checkStagingCapacity refuses a new staging while stagingMaxPending stagings
// (finished, unfinished or being published) or stagingMaxPendingBytes are
// pending. Two uploads racing past the check can overshoot by one package
// each, which the bound tolerates: it is there to stop repeated uploads from
// filling the update volume, not to account bytes exactly.
func checkStagingCapacity(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stagingStorageError("read staging directory", err)
	}
	count, total := 0, int64(0)
	for _, entry := range entries {
		if _, ok := stagingEntryID(entry.Name()); !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		count++
		total += info.Size()
	}
	if count >= stagingMaxPending || total >= stagingMaxPendingBytes {
		return ErrStagingFull
	}
	return nil
}

func stagingStorageError(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrStagingStorage, op, err)
}

// readErrorReader 记住读取上传时的错误（io.EOF 除外），把它与写暂存文件的错误分开。
type readErrorReader struct {
	r   io.Reader
	err error
}

func (r *readErrorReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return n, err
}

// StagedPackagePath returns the file of the staged package id. An id that is
// not exactly what StagePackage hands out, or that names nothing, reports
// ErrStagingNotFound; a staging older than ttl is removed and reports
// ErrStagingExpired.
func StagedPackagePath(manifestPath, id string, now time.Time, ttl time.Duration) (string, error) {
	path, err := stagedPath(manifestPath, id)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrStagingNotFound
	}
	if err != nil {
		return "", fmt.Errorf("stat staged package: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", ErrStagingNotFound
	}
	if stagingExpired(info, now, ttl) {
		_ = os.Remove(path)
		return "", ErrStagingExpired
	}
	return path, nil
}

// DiscardStagedPackage removes the staged package id.
func DiscardStagedPackage(manifestPath, id string) error {
	path, err := stagedPath(manifestPath, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrStagingNotFound
		}
		return fmt.Errorf("discard staged package: %w", err)
	}
	return nil
}

// CleanupStagedPackages removes the stagings, and the part files of uploads
// that never finished, last modified more than ttl before now. A claim (a
// staging being published) is left for twice ttl, so a publish confirmed just
// before its staging expired is not pulled from under it; an older claim was
// left by a publish that never finished. A missing staging directory is not
// an error. Files that are not stagings are left alone.
func CleanupStagedPackages(manifestPath string, now time.Time, ttl time.Duration) (int, error) {
	dir := StagingDir(manifestPath)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read staging directory: %w", err)
	}
	removed := 0
	var firstErr error
	for _, entry := range entries {
		if _, ok := stagingEntryID(entry.Name()); !ok {
			continue
		}
		expiry := ttl
		if strings.HasSuffix(entry.Name(), stagingClaimSuffix) {
			expiry = 2 * ttl
		}
		info, err := entry.Info()
		if err != nil || !stagingExpired(info, now, expiry) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove expired staging: %w", err)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// PublishStagedPackage runs PublishDesktopPackage on the staged package id:
// with opts.DryRun it is the preflight, otherwise the publish. Each call
// verifies the staged file from scratch; nothing from an earlier preflight is
// trusted. A successful publish removes the staging. A failed one keeps it, so
// the operator can retry (for example with AllowDowngrade, or once a
// concurrent publish released the lock) until it expires. Expired stagings are
// cleaned first.
//
// A publish first claims the staging by renaming it, so of two confirms of one
// id only one publishes; the other reports ErrStagingNotFound. A failed
// publish renames the claim back. A preview claims nothing: previews only
// read, and any number may run side by side.
func PublishStagedPackage(manifestPath, filesDir, id string, opts PublishOptions, ttl time.Duration) (*PublishResult, error) {
	now := opts.now()
	_, _ = CleanupStagedPackages(manifestPath, now, ttl)
	path, err := StagedPackagePath(manifestPath, id, now, ttl)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		return PublishDesktopPackage(manifestPath, filesDir, path, opts)
	}
	claimed := path + stagingClaimSuffix
	if err := os.Rename(path, claimed); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrStagingNotFound
		}
		return nil, fmt.Errorf("claim staged package: %w", err)
	}
	result, err := PublishDesktopPackage(manifestPath, filesDir, claimed, opts)
	if err != nil {
		// 认领时文件名改了、修改时间没变，恢复后有效期照旧。
		_ = os.Rename(claimed, path)
		return nil, err
	}
	_ = os.Remove(claimed)
	return result, nil
}

// StagePackage stages r for this store's manifest; see the package function.
func (s *Store) StagePackage(r io.Reader) (StagedPackage, error) {
	return StagePackage(s.manifestPath, r, s.opts.now(), DefaultStagingTTL)
}

// PublishStagedPackage previews (dryRun) or publishes the staged package id
// into this store's manifest, validating with the options the store reloads
// with; see the package function. The store picks the new manifest up on its
// next snapshot.
func (s *Store) PublishStagedPackage(id string, dryRun, allowDowngrade bool) (*PublishResult, error) {
	opts := PublishOptions{CatalogOptions: s.opts, DryRun: dryRun, AllowDowngrade: allowDowngrade}
	return PublishStagedPackage(s.manifestPath, s.filesDir, id, opts, DefaultStagingTTL)
}

// DiscardStagedPackage removes the staged package id of this store's manifest.
func (s *Store) DiscardStagedPackage(id string) error {
	return DiscardStagedPackage(s.manifestPath, id)
}

func stagedPath(manifestPath, id string) (string, error) {
	if !validStagingID(id) {
		return "", ErrStagingNotFound
	}
	return filepath.Join(StagingDir(manifestPath), id), nil
}

// stagingEntryID returns the staging id of a file in the staging directory: a
// staging, an unfinished upload (.part) or a staging being published (.claim).
func stagingEntryID(name string) (string, bool) {
	for _, suffix := range []string{stagingPartSuffix, stagingClaimSuffix} {
		if id, ok := strings.CutSuffix(name, suffix); ok {
			return id, validStagingID(id)
		}
	}
	return name, validStagingID(name)
}

func validStagingID(id string) bool {
	if len(id) != 2*stagingIDBytes {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func stagingExpired(info fs.FileInfo, now time.Time, ttl time.Duration) bool {
	return now.Sub(info.ModTime()) > ttl
}
