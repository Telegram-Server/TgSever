package updatecdn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PublishOptions 是 PublishDesktopPackage 的参数。CatalogOptions 与服务加载清单时
// 使用的完全相同：包按它验签，新清单也按它校验。
type PublishOptions struct {
	CatalogOptions
	// DryRun 只做全部校验并返回将要写入的条目，不写任何文件。
	DryRun bool
	// AllowDowngrade 允许发布 build 不大于现有同平台同通道条目的包（用于撤回坏版本）。
	AllowDowngrade bool
}

// PublishResult 描述一次发布（或只校验时将要进行的发布）。
type PublishResult struct {
	// Platform、Channel 是条目在清单 desktop 下的键，均从包内容推导。
	Platform string
	Channel  string
	// Release 是写入（或将写入）清单的条目。
	Release DesktopRelease
	// Package 是包验签得到的元数据。
	Package VerifiedPackage
	// Previous 是发布前同平台同通道的条目，没有时为 nil。
	Previous *DesktopRelease
	// ManifestUpdated 表示清单写入了新条目（只校验模式下为“将会写入”）。
	ManifestUpdated bool
	// FileCopied 表示包被拷进了文件目录（只校验模式下为“将会拷入”）。清单条目已是
	// 这个包、只是文件缺失时，只拷文件、不改清单。
	FileCopied bool
	// Unchanged 表示清单里已经是完全相同的启用条目且包文件已在文件目录中：重复发布，
	// 没有（或不需要）写任何文件。
	Unchanged bool
}

// PublishDesktopPackage 发布一个 v2 桌面更新包：先按 opts 完整验签，再从包内容推导
// 平台键、通道、build、大小与 SHA256，按 v2 规则生成文件名，把包拷进 filesDir，最后
// 以“同目录临时文件 + 改名”原子替换 manifestPath 指向的清单。清单不存在时创建一份新
// 清单；其余桌面条目与 apps 条目原样保留。
//
// 新清单在改名之前先按 LoadCatalog 的规则（同一组 CatalogOptions）完整加载一遍，失败
// 则什么都不替换。验签失败、目标没有平台键、通道不是 stable/beta、同名文件内容不同、
// 版本回退（未设 AllowDowngrade）时直接拒绝，不改动任何文件；后续步骤失败时删除本次
// 新拷入的包与临时文件。
//
// 同一时刻只允许一个发布：写入模式会在清单旁创建 <manifest>.lock，已存在时拒绝。
func PublishDesktopPackage(manifestPath, filesDir, packagePath string, opts PublishOptions) (*PublishResult, error) {
	pkg, sum, size, err := verifyPublishPackage(packagePath, opts.RootPublicKey, opts.now())
	if err != nil {
		return nil, err
	}
	platform, channel, err := packageEntryKey(pkg)
	if err != nil {
		return nil, err
	}
	release := DesktopRelease{
		Build:  uint64(pkg.Base()),
		File:   desktopPackageFileName(pkg),
		SHA256: hex.EncodeToString(sum[:]),
		Size:   size,
	}
	result := &PublishResult{Platform: platform, Channel: channel, Release: release, Package: pkg}

	if !opts.DryRun {
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
			return nil, fmt.Errorf("create manifest directory: %w", err)
		}
		unlock, err := lockManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}

	manifest, manifestInfo, err := readManifestFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		manifest, manifestInfo, err = Manifest{SchemaVersion: ManifestSchemaVersion}, nil, nil
	}
	if err != nil {
		return nil, err
	}
	entryUnchanged := false
	if previous, ok := manifest.Desktop[platform][channel]; ok {
		result.Previous = &previous
		// 停用的条目同样参与回退判断：被撤下的坏版本不能因为重跑发布而悄悄重新启用。
		entryUnchanged = !previous.Disabled && sameDesktopRelease(previous, release)
		if !entryUnchanged && release.Build <= previous.Build && !opts.AllowDowngrade {
			state := ""
			if previous.Disabled {
				state = " (disabled)"
			}
			return nil, fmt.Errorf("desktop.%s.%s: build %d is not newer than the published build %d%s (allow a downgrade explicitly to roll back or re-enable)",
				platform, channel, release.Build, previous.Build, state)
		}
	}
	target := filepath.Join(filesDir, release.File)
	fileExists, err := checkPublishedFile(target, release)
	if err != nil {
		return nil, err
	}
	result.ManifestUpdated = !entryUnchanged
	result.FileCopied = !fileExists
	result.Unchanged = !result.ManifestUpdated && !result.FileCopied

	if !entryUnchanged {
		if manifest.Desktop == nil {
			manifest.Desktop = make(map[string]map[string]DesktopRelease)
		}
		if manifest.Desktop[platform] == nil {
			manifest.Desktop[platform] = make(map[string]DesktopRelease)
		}
		manifest.Desktop[platform][channel] = release
	}
	data, err := encodeManifest(manifest)
	if err != nil {
		return nil, err
	}

	if opts.DryRun {
		// 包还没有拷进 filesDir：用刚验过的包代替它，其余条目照常从 filesDir 校验。
		// 新清单从编码后的字节重新解码，与服务读到的完全相同。
		candidate, err := decodeManifest(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("new manifest is invalid: %w", err)
		}
		verified := map[string]fileRecord{release.File: {path: target, name: release.File, sha256: release.SHA256, size: size, pkg: pkg}}
		if _, err := newCatalog(candidate, filesDir, opts.CatalogOptions, verified); err != nil {
			return nil, fmt.Errorf("new manifest is invalid: %w", err)
		}
		return result, nil
	}

	copied := false
	if !fileExists {
		if copied, err = copyPublishPackage(packagePath, filesDir, release); err != nil {
			return nil, err
		}
		result.FileCopied = copied
	}
	if !entryUnchanged {
		if err := replaceManifest(manifestPath, manifestInfo, data, filesDir, opts.CatalogOptions); err != nil {
			if copied {
				_ = os.Remove(target)
			}
			return nil, err
		}
	}
	result.Unchanged = !result.ManifestUpdated && !result.FileCopied
	return result, nil
}

// encodeManifest 以两格缩进编码清单，不做 HTML 转义，免得把运营者手写的 URL 与说明
// 改写成 & 之类。
func encodeManifest(manifest Manifest) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// verifyPublishPackage 打开待发布的包并完整验签，返回包信息、整文件 SHA256 与大小。
func verifyPublishPackage(path string, root ed25519.PublicKey, now time.Time) (VerifiedPackage, [sha256.Size]byte, int64, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return VerifiedPackage{}, sum, 0, fmt.Errorf("open package: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return VerifiedPackage{}, sum, 0, fmt.Errorf("stat package: %w", err)
	}
	if !info.Mode().IsRegular() {
		return VerifiedPackage{}, sum, 0, fmt.Errorf("package %q is not a regular file", path)
	}
	pkg, sum, err := VerifyDesktopPackageStream(f, info.Size(), root, now)
	if err != nil {
		return VerifiedPackage{}, sum, 0, fmt.Errorf("package %q: %w", path, err)
	}
	return pkg, sum, info.Size(), nil
}

// packageEntryKey 从包内的目标与通道推导清单 desktop 下的平台键与通道。
func packageEntryKey(pkg VerifiedPackage) (platform, channel string, err error) {
	switch pkg.Channel {
	case PackageChannelStable, PackageChannelBeta:
		channel = pkg.Channel.String()
	default:
		return "", "", fmt.Errorf("package channel %s cannot be published: the update server only serves stable and beta", pkg.Channel)
	}
	target := pkg.Target.String()
	for key, value := range desktopPlatformTargets {
		if value == target {
			platform = key
		}
	}
	if platform == "" {
		return "", "", fmt.Errorf("package target %s has no desktop platform key", target)
	}
	if pkg.Base() == 0 {
		return "", "", fmt.Errorf("package version base must be positive")
	}
	return platform, channel, nil
}

// desktopPackageFileName 按 v2 规则 td-update-{os}-{arch}-{base}[-beta] 生成文件名。
func desktopPackageFileName(pkg VerifiedPackage) string {
	name := fmt.Sprintf("td-update-%s-%d", pkg.Target, pkg.Base())
	if pkg.Channel == PackageChannelBeta {
		name += "-beta"
	}
	return name
}

// sameDesktopRelease 判断现有条目是否就是这个包：同 build、同文件、同 SHA256，且大小
// 一致（未写大小视为一致）。
func sameDesktopRelease(previous, release DesktopRelease) bool {
	return previous.Build == release.Build &&
		previous.File == release.File &&
		strings.EqualFold(previous.SHA256, release.SHA256) &&
		(previous.Size == 0 || previous.Size == release.Size)
}

// ErrPublishInProgress 表示 <manifest>.lock 已存在：另一个发布（命令行或管理后台）
// 正在进行，或者上一次发布异常退出留下了锁文件。
var ErrPublishInProgress = errors.New("another publish is in progress")

// lockManifest 创建 <manifest>.lock 以排除并发发布；返回的函数删除它。
func lockManifest(manifestPath string) (func(), error) {
	lockPath := manifestPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w (%s exists; remove it if no publish is running)", ErrPublishInProgress, lockPath)
		}
		return nil, fmt.Errorf("lock manifest: %w", err)
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return func() { _ = os.Remove(lockPath) }, nil
}

// checkPublishedFile 检查文件目录中的同名文件：不存在返回 false；内容与包完全相同返回
// true（重复发布）；内容不同则拒绝，已发布的包不可变。
func checkPublishedFile(path string, release DesktopRelease) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open published file %q: %w", release.File, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat published file %q: %w", release.File, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("published file %q is not a regular file", release.File)
	}
	conflict := fmt.Errorf("published file %q already exists with different content; published packages are immutable", release.File)
	if info.Size() != release.Size {
		return false, conflict
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false, fmt.Errorf("read published file %q: %w", release.File, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != release.SHA256 {
		return false, conflict
	}
	return true, nil
}

// copyPublishPackage 把包拷进 filesDir：先写同目录临时文件并核对 SHA256（防止包在验签后
// 被替换），再用硬链接放到最终文件名——硬链接在目标已存在时失败，不会覆盖已发布的包。
// 文件系统不支持硬链接时退回到排他创建目标文件再拷贝。返回是否新建了文件；目标已被
// 他人放入相同内容时返回 false。
func copyPublishPackage(packagePath, filesDir string, release DesktopRelease) (bool, error) {
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return false, fmt.Errorf("create files directory: %w", err)
	}
	src, err := os.Open(packagePath)
	if err != nil {
		return false, fmt.Errorf("open package: %w", err)
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filesDir, "."+release.File+".*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary package file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(src, release.Size+1))
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("copy package to %q: %w", release.File, err)
	}
	if written != release.Size || hex.EncodeToString(hash.Sum(nil)) != release.SHA256 {
		return false, fmt.Errorf("package %q changed while publishing", packagePath)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return false, fmt.Errorf("chmod package file: %w", err)
	}
	target := filepath.Join(filesDir, release.File)
	err = os.Link(tmpPath, target)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		err = copyExclusive(tmpPath, target, release)
	}
	if errors.Is(err, fs.ErrExist) {
		// 校验之后有人放入了同名文件：内容相同仍算重复发布，不同则拒绝。
		if _, checkErr := checkPublishedFile(target, release); checkErr != nil {
			return false, checkErr
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("place package %q: %w", release.File, err)
	}
	return true, nil
}

// copyExclusive 以 O_EXCL 新建 target 并从 src 拷贝，用于不支持硬链接的文件系统；
// target 已存在时返回满足 fs.ErrExist 的错误，失败时删除已建的 target。拷贝期间
// target 不完整，但清单尚未引用它，服务不会提供它。
func copyExclusive(src, target string, release DesktopRelease) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("copy package %q: %w", release.File, err)
	}
	return nil
}

// replaceManifest 把新清单写到同目录临时文件，按 LoadCatalog 的规则完整加载一遍，
// 通过后改名替换原清单。任何一步失败都删除临时文件，原清单保持不变。Windows 上目标
// 正被其他进程打开时改名可能失败，这时返回错误，重试即可。previous 是原清单的文件
// 信息（不存在时为 nil），新清单沿用其权限位，并尽量沿用属主（见 copyOwner）。
func replaceManifest(manifestPath string, previous fs.FileInfo, data []byte, filesDir string, opts CatalogOptions) error {
	dir, base := filepath.Split(manifestPath)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()

	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write temporary manifest: %w", err)
	}
	mode := fs.FileMode(0o644)
	if previous != nil {
		mode = previous.Mode().Perm()
		copyOwner(tmpPath, previous)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod temporary manifest: %w", err)
	}
	if _, err := LoadCatalog(tmpPath, filesDir, opts); err != nil {
		return fmt.Errorf("new manifest is invalid: %w", err)
	}
	if err := os.Rename(tmpPath, manifestPath); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	renamed = true
	return nil
}
