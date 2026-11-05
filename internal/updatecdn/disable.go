package updatecdn

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
)

// ErrDesktopEntryNotFound 表示清单里没有要停用或启用的 desktop.<platform>.<channel>
// 条目（或清单本身不存在）。
var ErrDesktopEntryNotFound = errors.New("desktop entry not found")

// SetDisabledOptions 是 SetDesktopReleaseDisabled 的参数。CatalogOptions 与服务加载
// 清单时使用的完全相同，新清单按它校验。
type SetDisabledOptions struct {
	CatalogOptions
	// DryRun 只做全部校验并返回操作之后的条目，不取锁、不写任何文件。
	DryRun bool
}

// SetDisabledResult 描述一次停用或启用。
type SetDisabledResult struct {
	// Platform、Channel 是条目在清单 desktop 下的键。
	Platform string
	Channel  string
	// Release 是操作之后（只校验模式下为“将会”）清单里的条目。
	Release DesktopRelease
	// Unchanged 表示条目本来就是目标状态，没有写任何文件。
	Unchanged bool
}

// SetDesktopReleaseDisabled 把清单中 desktop.<platform>.<channel> 条目的 disabled 设为
// disabled，其余条目与 apps 原样保留。与 PublishDesktopPackage 共用 <manifest>.lock
// （已存在时返回满足 ErrPublishInProgress 的错误），新清单写同目录临时文件，按
// LoadCatalog 的规则（同一组 opts）完整加载通过后才改名替换；任何失败都不改动文件。
//
// 条目不存在时返回满足 ErrDesktopEntryNotFound 的错误；已是目标状态时返回
// Unchanged，不写任何文件。重新启用不做版本回退检查（这是显式操作，由调用方要求
// 确认），但被启用的包必须仍在文件目录中且通过验签，否则拒绝。
//
// opts.DryRun 时做同样的校验（新清单完整加载一遍）但不取锁、不写文件，与发布的
// 只校验模式一致。
func SetDesktopReleaseDisabled(manifestPath, filesDir, platform, channel string, disabled bool, opts SetDisabledOptions) (*SetDisabledResult, error) {
	if platform == "android" {
		return nil, fmt.Errorf("desktop.%s.%s: %w (use SetAppReleaseDisabled for apps entries)", platform, channel, ErrDesktopEntryNotFound)
	}
	if !opts.DryRun {
		unlock, err := lockManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}

	manifest, manifestInfo, err := readManifestFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("desktop.%s.%s: %w (no manifest at %s)", platform, channel, ErrDesktopEntryNotFound, manifestPath)
	}
	if err != nil {
		return nil, err
	}
	release, ok := manifest.Desktop[platform][channel]
	if !ok {
		return nil, fmt.Errorf("desktop.%s.%s: %w", platform, channel, ErrDesktopEntryNotFound)
	}
	result := &SetDisabledResult{Platform: platform, Channel: channel, Release: release}
	if release.Disabled == disabled {
		result.Unchanged = true
		return result, nil
	}
	release.Disabled = disabled
	manifest.Desktop[platform][channel] = release
	data, err := encodeManifest(manifest)
	if err != nil {
		return nil, err
	}
	result.Release = release
	if opts.DryRun {
		// 新清单从编码后的字节重新解码，与服务读到的完全相同。
		candidate, err := decodeManifest(bytes.NewReader(data))
		if err == nil {
			_, err = newCatalog(candidate, filesDir, opts.CatalogOptions, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("new manifest is invalid: %w", err)
		}
		return result, nil
	}
	if err := replaceManifest(manifestPath, manifestInfo, data, filesDir, opts.CatalogOptions); err != nil {
		return nil, err
	}
	return result, nil
}

// SetAppDisabledResult 描述一次 apps 条目的停用或启用，字段与 SetDisabledResult 同义。
type SetAppDisabledResult struct {
	Platform  string
	Channel   string
	Release   ApplicationRelease
	Unchanged bool
}

// SetAppReleaseDisabled 是 SetDesktopReleaseDisabled 的 apps 版本：设置
// apps.<platform>.<channel> 的 disabled，锁、校验与写入规则相同。重新启用带 file 的
// Android 条目时，安装包必须仍在文件目录中，且与条目的大小、SHA256、包名、版本与签名一致。
func SetAppReleaseDisabled(manifestPath, filesDir, platform, channel string, disabled bool, opts SetDisabledOptions) (*SetAppDisabledResult, error) {
	if !opts.DryRun {
		unlock, err := lockManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	manifest, manifestInfo, err := readManifestFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("apps.%s.%s: %w (no manifest at %s)", platform, channel, ErrDesktopEntryNotFound, manifestPath)
	}
	if err != nil {
		return nil, err
	}
	release, ok := manifest.Apps[platform][channel]
	if !ok {
		return nil, fmt.Errorf("apps.%s.%s: %w", platform, channel, ErrDesktopEntryNotFound)
	}
	result := &SetAppDisabledResult{Platform: platform, Channel: channel, Release: release}
	if release.Disabled == disabled {
		result.Unchanged = true
		return result, nil
	}
	release.Disabled = disabled
	manifest.Apps[platform][channel] = release
	data, err := encodeManifest(manifest)
	if err != nil {
		return nil, err
	}
	result.Release = release
	if opts.DryRun {
		candidate, err := decodeManifest(bytes.NewReader(data))
		if err == nil {
			_, err = newCatalog(candidate, filesDir, opts.CatalogOptions, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("new manifest is invalid: %w", err)
		}
		return result, nil
	}
	if err := replaceManifest(manifestPath, manifestInfo, data, filesDir, opts.CatalogOptions); err != nil {
		return nil, err
	}
	return result, nil
}

// SetAppReleaseDisabled previews (dryRun) or applies disabling or re-enabling
// apps.<platform>.<channel> in this store's manifest; see the package function.
func (s *Store) SetAppReleaseDisabled(platform, channel string, disabled, dryRun bool) (*SetAppDisabledResult, error) {
	opts := SetDisabledOptions{CatalogOptions: s.opts, DryRun: dryRun}
	return SetAppReleaseDisabled(s.manifestPath, s.filesDir, platform, channel, disabled, opts)
}

// SetDesktopReleaseDisabled previews (dryRun) or applies disabling or
// re-enabling desktop.<platform>.<channel> in this store's manifest,
// validating with the options the store reloads with; see the package
// function. The store picks the new manifest up on its next snapshot.
func (s *Store) SetDesktopReleaseDisabled(platform, channel string, disabled, dryRun bool) (*SetDisabledResult, error) {
	opts := SetDisabledOptions{CatalogOptions: s.opts, DryRun: dryRun}
	return SetDesktopReleaseDisabled(s.manifestPath, s.filesDir, platform, channel, disabled, opts)
}
