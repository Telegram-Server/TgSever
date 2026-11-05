package updatecdn

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"telesrv/internal/apkinfo"
)

// 发布 Android 安装包：把 APK 拷进文件目录，写 apps.android.<channel> 条目。版本、包名与
// 签名证书都从 APK 读出，不由运营者填写；运营者只给通道、更新说明、是否强制、备用链接。
//
// 身份规则：清单里已有的 Android 条目（任何通道，停用的也算）记下了包名与签名证书，新包
// 必须与它们一致——包名不同是传错了应用，签名证书不同则已安装的客户端根本装不上（Android
// 只允许同一签名覆盖安装）。确实要换签名密钥时，先手工从清单里删掉旧条目。

var (
	// ErrAndroidIdentityMismatch 表示新包的包名或签名证书与清单里已有的 Android 条目不同。
	ErrAndroidIdentityMismatch = errors.New("the package does not match the published Android app")
	// ErrAndroidReleaseInvalid 表示通道、更新说明或备用链接不合法。
	ErrAndroidReleaseInvalid = errors.New("invalid Android release")
)

// AndroidReleaseMeta 是发布 Android 安装包时运营者填写的部分。
type AndroidReleaseMeta struct {
	// Channel 是 stable、beta 或 alpha。
	Channel string
	// Notes 是按语言给出的更新说明（en、zh、ru 等），至少一种。
	Notes map[string]string
	// CanNotSkip 为 true 时客户端显示强制更新页。
	CanNotSkip bool
	// URL 是可选的备用下载链接，只在客户端拿不到 document 时（强制更新页）使用。
	URL string
}

// AndroidInspection 是一个安装包的预检结果：从 APK 读出的元数据、整个文件的 SHA256 与
// 大小、与已发布应用的身份是否一致，以及各通道当前的条目。
type AndroidInspection struct {
	APK    apkinfo.Info
	SHA256 string
	Size   int64
	// Current 是 apps.android 下各通道当前的条目（含停用）。
	Current map[string]ApplicationRelease
}

// AndroidPublishResult 描述一次 Android 发布（或只校验时将要进行的发布）。
type AndroidPublishResult struct {
	Channel  string
	Release  ApplicationRelease
	APK      apkinfo.Info
	Previous *ApplicationRelease
	// ManifestUpdated、FileCopied、Unchanged 与 PublishResult 同义。同一个包只改了说明、
	// 强制或链接时 ManifestUpdated 为 true、FileCopied 为 false。
	ManifestUpdated bool
	FileCopied      bool
	Unchanged       bool
	// Downgrade 表示被替换的条目版本不低于新包（回滚，或重新发布被停用的同一版本）。
	Downgrade bool
}

// InspectAndroidPackage 解析 apkPath 并核对它与 manifestPath 里已发布的 Android 应用的
// 身份（包名、签名证书）。清单不存在时视为还没有发布过。不写任何文件。
func InspectAndroidPackage(manifestPath, apkPath string) (*AndroidInspection, error) {
	record, info, err := inspectAndroidFile(apkPath)
	if err != nil {
		return nil, fmt.Errorf("package %q: %w", apkPath, err)
	}
	manifest, _, err := readManifestFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		manifest, err = Manifest{SchemaVersion: ManifestSchemaVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkAndroidIdentity(manifest, info); err != nil {
		return nil, err
	}
	if _, err := androidReleaseID(info); err != nil {
		return nil, err
	}
	current := make(map[string]ApplicationRelease, len(manifest.Apps["android"]))
	for channel, release := range manifest.Apps["android"] {
		current[channel] = release
	}
	return &AndroidInspection{APK: info, SHA256: record.sha256, Size: record.size, Current: current}, nil
}

// PublishAndroidPackage 发布一个 Android 安装包到 apps.android.<meta.Channel>：解析 APK、
// 核对身份与版本（不新于当前条目时需要 AllowDowngrade；同一个包只改说明等字段不算回退）、
// 把包以 android-<包名>-<versionCode>.apk 拷进 filesDir，再原子替换清单。新清单在替换前
// 按 LoadCatalog 的规则完整加载一遍。与桌面发布共用 <manifest>.lock；DryRun 时只校验，
// 不取锁、不写文件。条目原有的 url_by_source 保留。
func PublishAndroidPackage(manifestPath, filesDir, apkPath string, meta AndroidReleaseMeta, opts PublishOptions) (*AndroidPublishResult, error) {
	meta, err := normalizeAndroidMeta(meta)
	if err != nil {
		return nil, err
	}
	record, info, err := inspectAndroidFile(apkPath)
	if err != nil {
		return nil, fmt.Errorf("package %q: %w", apkPath, err)
	}
	id, err := androidReleaseID(info)
	if err != nil {
		return nil, err
	}
	release := ApplicationRelease{
		ID:          id,
		Version:     info.VersionName,
		URL:         meta.URL,
		CanNotSkip:  meta.CanNotSkip,
		Notes:       meta.Notes,
		File:        androidPackageFileName(info),
		SHA256:      record.sha256,
		Size:        record.size,
		PackageName: info.PackageName,
		VersionCode: info.VersionCode,
		CertSHA256:  strings.ToLower(info.SignerCertSHA256),
	}
	if _, ok := parseVersion(release.Version); !ok {
		return nil, fmt.Errorf("%w: versionName %q contains no numeric version", ErrAndroidReleaseInvalid, release.Version)
	}
	result := &AndroidPublishResult{Channel: meta.Channel, Release: release, APK: info}

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
	if err := checkAndroidIdentity(manifest, info); err != nil {
		return nil, err
	}

	entryUnchanged := false
	if previous, ok := manifest.Apps["android"][meta.Channel]; ok {
		result.Previous = &previous
		release.URLBySource = previous.URLBySource
		result.Release = release
		samePackage := strings.EqualFold(previous.SHA256, release.SHA256) && previous.File == release.File
		entryUnchanged = !previous.Disabled && samePackage && sameAndroidMeta(previous, release)
		// 同一个包（启用中）只改说明、强制或链接：不是回退。
		metadataEdit := !previous.Disabled && samePackage
		if !entryUnchanged && !metadataEdit && !androidNewer(release, previous) {
			result.Downgrade = true
			if !opts.AllowDowngrade {
				state := ""
				if previous.Disabled {
					state = " (disabled)"
				}
				return nil, fmt.Errorf("apps.android.%s: version %s (%d) is not newer than the published %s (%d)%s (allow a downgrade explicitly to roll back or re-enable)",
					meta.Channel, release.Version, release.VersionCode, previous.Version, previous.VersionCode, state)
			}
		}
	}
	file := publishedFile{File: release.File, Size: release.Size, SHA256: release.SHA256}
	target := filepath.Join(filesDir, release.File)
	fileExists, err := checkPublishedFile(target, file)
	if err != nil {
		return nil, err
	}
	result.ManifestUpdated = !entryUnchanged
	result.FileCopied = !fileExists
	result.Unchanged = !result.ManifestUpdated && !result.FileCopied

	if !entryUnchanged {
		if manifest.Apps == nil {
			manifest.Apps = make(map[string]map[string]ApplicationRelease)
		}
		if manifest.Apps["android"] == nil {
			manifest.Apps["android"] = make(map[string]ApplicationRelease)
		}
		manifest.Apps["android"][meta.Channel] = release
	}
	data, err := encodeManifest(manifest)
	if err != nil {
		return nil, err
	}

	if opts.DryRun {
		candidate, err := decodeManifest(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("new manifest is invalid: %w", err)
		}
		staged := record
		staged.path, staged.name, staged.apk = target, release.File, &info
		verified := map[string]fileRecord{release.File: staged}
		if _, err := newCatalog(candidate, filesDir, opts.CatalogOptions, verified); err != nil {
			return nil, fmt.Errorf("new manifest is invalid: %w", err)
		}
		return result, nil
	}

	copied := false
	if !fileExists {
		if copied, err = copyPublishPackage(apkPath, filesDir, file); err != nil {
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

// normalizeAndroidMeta 去掉空白、统一语言键（小写，_ 换成 -），校验通道、说明与链接。
func normalizeAndroidMeta(meta AndroidReleaseMeta) (AndroidReleaseMeta, error) {
	meta.Channel = strings.ToLower(strings.TrimSpace(meta.Channel))
	if !validAppChannel(meta.Channel) {
		return meta, fmt.Errorf("%w: channel must be stable, beta or alpha", ErrAndroidReleaseInvalid)
	}
	notes := make(map[string]string, len(meta.Notes))
	for lang, text := range meta.Notes {
		lang = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(lang), "_", "-"))
		text = strings.TrimSpace(text)
		if lang == "" || text == "" {
			continue
		}
		if len(lang) > 16 || len(text) > 4096 {
			return meta, fmt.Errorf("%w: release notes language %q is too long or its text exceeds 4096 bytes", ErrAndroidReleaseInvalid, lang)
		}
		notes[lang] = text
	}
	if len(notes) == 0 {
		return meta, fmt.Errorf("%w: release notes are required in at least one language", ErrAndroidReleaseInvalid)
	}
	meta.Notes = notes
	meta.URL = strings.TrimSpace(meta.URL)
	if meta.URL != "" {
		if err := validateDownloadURL(meta.URL); err != nil {
			return meta, fmt.Errorf("%w: url: %w", ErrAndroidReleaseInvalid, err)
		}
	}
	return meta, nil
}

// checkAndroidIdentity 要求新包的包名与签名证书与清单里每个记下了它们的 Android 条目一致。
func checkAndroidIdentity(manifest Manifest, info apkinfo.Info) error {
	channels := make([]string, 0, len(manifest.Apps["android"]))
	for channel := range manifest.Apps["android"] {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		existing := manifest.Apps["android"][channel]
		if existing.PackageName != "" && existing.PackageName != info.PackageName {
			return fmt.Errorf("%w: package name %s differs from %s published on apps.android.%s", ErrAndroidIdentityMismatch, info.PackageName, existing.PackageName, channel)
		}
		if existing.CertSHA256 != "" && !strings.EqualFold(existing.CertSHA256, info.SignerCertSHA256) {
			return fmt.Errorf("%w: signing certificate %s differs from %s published on apps.android.%s; installed clients could not install it",
				ErrAndroidIdentityMismatch, strings.ToLower(info.SignerCertSHA256), strings.ToLower(existing.CertSHA256), channel)
		}
	}
	return nil
}

// androidReleaseID 用 versionCode 作为 help.appUpdate 的 id（int32）。
func androidReleaseID(info apkinfo.Info) (int, error) {
	if info.VersionCode <= 0 || info.VersionCode > math.MaxInt32 {
		return 0, fmt.Errorf("%w: versionCode %d does not fit help.appUpdate's 32-bit id", ErrAndroidReleaseInvalid, info.VersionCode)
	}
	return int(info.VersionCode), nil
}

func androidPackageFileName(info apkinfo.Info) string {
	return fmt.Sprintf("android-%s-%d.apk", info.PackageName, info.VersionCode)
}

// androidNewer 判断 release 是否新于 previous：两者都有 versionCode 时比较它，否则比较
// versionName。
func androidNewer(release, previous ApplicationRelease) bool {
	if previous.VersionCode > 0 {
		return release.VersionCode > previous.VersionCode
	}
	return compareVersions(release.Version, previous.Version) > 0
}

// sameAndroidMeta 判断两个指向同一个包的条目其余字段是否也相同。
func sameAndroidMeta(a, b ApplicationRelease) bool {
	if a.ID != b.ID || a.Version != b.Version || a.URL != b.URL || a.CanNotSkip != b.CanNotSkip || len(a.Notes) != len(b.Notes) {
		return false
	}
	for lang, text := range a.Notes {
		if b.Notes[lang] != text {
			return false
		}
	}
	return a.PackageName == b.PackageName && a.VersionCode == b.VersionCode && strings.EqualFold(a.CertSHA256, b.CertSHA256) && a.Size == b.Size
}
