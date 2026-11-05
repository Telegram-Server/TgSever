package admin

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/updatecdn"
)

// Client updates: the desktop update catalog the server publishes to custom
// Telegram Desktop builds (internal/updatecdn, docs/update-service.md).
//
// 管理后台只在进程内更新服务启用时（TELESRV_UPDATE_MANIFEST）有目录可读写；外部
// telesrv-update 模式下只报告状态，未配置时报告“未启用”。首次启用仍走命令行与
// 环境变量。

// 更新服务的三种模式，见 ClientUpdateStatus.Mode。
const (
	ClientUpdatesDisabled  = "disabled"
	ClientUpdatesInProcess = "in_process"
	ClientUpdatesExternal  = "external"
)

// 客户端更新的命令动作，记入 admin_commands 与审计日志。预检是 dry-run 命令。
const (
	ActionPreflightClientUpdate = "updates.preflight"
	ActionPublishClientUpdate   = "updates.publish"
	ActionDisableClientUpdate   = "updates.disable"
	ActionEnableClientUpdate    = "updates.enable"
)

// 客户端更新写操作的稳定错误码，面板按它显示说明而不是匹配英文错误。
const (
	CodeUpdatesDisabled         = "UPDATES_DISABLED"
	CodeUpdatesExternal         = "UPDATES_EXTERNAL"
	CodeUpdateStagingNotFound   = "UPDATE_STAGING_NOT_FOUND"
	CodeUpdateStagingExpired    = "UPDATE_STAGING_EXPIRED"
	CodeUpdatePublishInProgress = "UPDATE_PUBLISH_IN_PROGRESS"
	CodeUpdatePackageTooLarge   = "UPDATE_PACKAGE_TOO_LARGE"
	// CodeUpdateStagingFull 表示同时暂存的上传已达上限；发布或等暂存过期后再上传。
	CodeUpdateStagingFull = "UPDATE_STAGING_FULL"
	// CodeUpdateUploadFailed 表示读取上传内容失败（断开、超时），是客户端一侧的问题，
	// 重新上传即可。
	CodeUpdateUploadFailed = "UPDATE_UPLOAD_FAILED"
	// CodeUpdateStagingStorage 表示服务器写暂存失败（磁盘满、目录不可写），需要运维处理。
	CodeUpdateStagingStorage = "UPDATE_STAGING_STORAGE_FAILED"
	// CodeUpdateEntryInvalid 表示停用或启用请求缺少 platform 或 channel。
	CodeUpdateEntryInvalid = "UPDATE_ENTRY_INVALID"
	// CodeUpdateEntryNotFound 表示要停用或启用的 desktop.<platform>.<channel> 不在清单里。
	CodeUpdateEntryNotFound = "UPDATE_ENTRY_NOT_FOUND"
	// CodeUpdatePackageRejected 是预检、发布或重新启用的其余失败：签名、平台、通道、
	// 版本回退、同名文件内容不同、包文件缺失、新清单无效。具体原因在错误文本里。
	CodeUpdatePackageRejected = "UPDATE_PACKAGE_REJECTED"
	// CodeUpdateAndroidIdentityMismatch 表示 APK 的包名或签名证书与已发布的 Android 应用不同：
	// 传错了应用，或换了签名密钥（已安装的客户端装不上）。
	CodeUpdateAndroidIdentityMismatch = "UPDATE_ANDROID_IDENTITY_MISMATCH"
	// CodeUpdateAndroidReleaseInvalid 表示发布 APK 时通道、更新说明或备用链接不合法，或者
	// 暂存的是 APK 却没给发布信息（反之亦然）。
	CodeUpdateAndroidReleaseInvalid = "UPDATE_ANDROID_RELEASE_INVALID"
)

// ClientUpdateCatalog is the in-process update catalog. *updatecdn.Store
// implements it: Status lists the served entries and the reload error, and
// the manifest path, files directory and load options are what a publish
// from the panel validates against, exactly as the store reloads. The staging
// methods stage an upload next to the manifest, preview or publish it with
// the store's own options, and drop it (see updatecdn.StagePackage).
// SetDesktopReleaseDisabled previews or applies disabling or re-enabling one
// entry under the same lock and validation (see
// updatecdn.SetDesktopReleaseDisabled).
type ClientUpdateCatalog interface {
	Status() (updatecdn.StoreStatus, error)
	ManifestPath() string
	FilesDir() string
	CatalogOptions() updatecdn.CatalogOptions
	StagePackage(r io.Reader) (updatecdn.StagedPackage, error)
	PublishStagedPackage(id string, dryRun, allowDowngrade bool) (*updatecdn.PublishResult, error)
	DiscardStagedPackage(id string) error
	SetDesktopReleaseDisabled(platform, channel string, disabled, dryRun bool) (*updatecdn.SetDisabledResult, error)
	// Android 安装包：按内容判断暂存的种类，预检（解析 APK、核对身份），发布到某个通道，
	// 停用或启用 apps 条目。
	StagedPackageKind(id string) (string, error)
	InspectStagedAndroidPackage(id string) (*updatecdn.AndroidInspection, error)
	PublishStagedAndroidPackage(id string, meta updatecdn.AndroidReleaseMeta, dryRun, allowDowngrade bool) (*updatecdn.AndroidPublishResult, error)
	SetAppReleaseDisabled(platform, channel string, disabled, dryRun bool) (*updatecdn.SetAppDisabledResult, error)
}

// ClientUpdateDocuments reports whether an installer package has been
// registered as a document yet (app/appupdates.Registry).
type ClientUpdateDocuments interface {
	Ready(ctx context.Context, sha256 string) (bool, error)
}

var _ ClientUpdateCatalog = (*updatecdn.Store)(nil)

// ClientUpdates configures the client update surface. Leave
// Dependencies.ClientUpdates nil when the update service is disabled. With a
// nil Catalog the updates are served by a separate telesrv-update (external
// mode) and the panel can only report that.
type ClientUpdates struct {
	// PublicURL 是对外下发给客户端的更新地址（autoupdate_url_prefix）。
	PublicURL string
	// ServiceURL 是外部模式下服务端访问 telesrv-update 的地址。
	ServiceURL string
	// Catalog 是进程内的更新目录；为 nil 表示外部模式。
	Catalog ClientUpdateCatalog
	// Documents 报告安装包是否已注册成文档；为 nil 时状态里不报告。
	Documents ClientUpdateDocuments
}

// ClientUpdateStatus is what the panel's client updates page shows.
type ClientUpdateStatus struct {
	Mode       string `json:"mode"`
	PublicURL  string `json:"public_url,omitempty"`
	ServiceURL string `json:"service_url,omitempty"`
	// ReloadError 非空时清单最近一次重新加载失败，Entries 来自上一份正常清单。
	ReloadError string `json:"reload_error,omitempty"`
	// MaxPackageSize 是进程内模式下可上传的包大小上限（字节）。面板在上传前据此检查，
	// 不必把超限的文件传到一半才被拒绝。
	MaxPackageSize int64               `json:"max_package_size,omitempty"`
	Entries        []ClientUpdateEntry `json:"entries"`
	// AndroidEntries 是 apps.android 下的条目（含停用）。
	AndroidEntries []ClientUpdateAndroidEntry `json:"android_entries"`
}

// ClientUpdateAndroidEntry is one apps.android.<channel> manifest entry.
type ClientUpdateAndroidEntry struct {
	Channel     string            `json:"channel"`
	ID          int               `json:"id"`
	Version     string            `json:"version"`
	VersionCode int64             `json:"version_code,omitempty"`
	PackageName string            `json:"package_name,omitempty"`
	File        string            `json:"file,omitempty"`
	SHA256      string            `json:"sha256,omitempty"`
	Size        int64             `json:"size,omitempty"`
	CertSHA256  string            `json:"cert_sha256,omitempty"`
	CanNotSkip  bool              `json:"can_not_skip"`
	Notes       map[string]string `json:"notes"`
	URL         string            `json:"url,omitempty"`
	Disabled    bool              `json:"disabled"`
	// DocumentReady 是安装包是否已注册成文档（只有带 file 的启用条目才有）；还没就绪时
	// 安卓客户端收到的是“没有更新”。
	DocumentReady *bool `json:"document_ready,omitempty"`
}

// ClientUpdateEntry is one desktop.<platform>.<channel> manifest entry.
type ClientUpdateEntry struct {
	Platform string `json:"platform"`
	Channel  string `json:"channel"`
	Build    uint64 `json:"build"`
	Version  string `json:"version,omitempty"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	// Size 对停用条目可能为 0（手写清单可以省略）。
	Size     int64 `json:"size,omitempty"`
	Disabled bool  `json:"disabled"`
	// Created 是 Packer 打包时间；停用条目的包不做校验，没有这一项。
	Created *time.Time `json:"created,omitempty"`
}

// ClientUpdateStatus reports the update service mode and, in process, every
// desktop entry (disabled ones included) and why the latest manifest reload
// failed, if it did. It does not fail when the service is disabled or
// external; those are states the page explains.
func (s *Service) ClientUpdateStatus(ctx context.Context) (ClientUpdateStatus, error) {
	status := ClientUpdateStatus{Mode: ClientUpdatesDisabled, Entries: []ClientUpdateEntry{}, AndroidEntries: []ClientUpdateAndroidEntry{}}
	if s == nil || s.clientUpdates == nil {
		return status, nil
	}
	cfg := s.clientUpdates
	status.PublicURL = cfg.PublicURL
	status.ServiceURL = cfg.ServiceURL
	if cfg.Catalog == nil {
		status.Mode = ClientUpdatesExternal
		return status, nil
	}
	status.Mode = ClientUpdatesInProcess
	status.MaxPackageSize = updatecdn.MaxDesktopPackageSize
	current, err := cfg.Catalog.Status()
	if err != nil {
		return ClientUpdateStatus{}, err
	}
	if current.ReloadErr != nil {
		status.ReloadError = current.ReloadErr.Error()
	}
	for _, entry := range current.Desktop {
		item := ClientUpdateEntry{
			Platform: entry.Platform,
			Channel:  entry.Channel,
			Build:    entry.Release.Build,
			Version:  entry.Release.Version,
			File:     entry.Release.File,
			SHA256:   entry.Release.SHA256,
			Size:     entry.Release.Size,
			Disabled: entry.Release.Disabled,
		}
		if !entry.Created.IsZero() {
			created := entry.Created.UTC()
			item.Created = &created
		}
		status.Entries = append(status.Entries, item)
	}
	for _, entry := range current.Apps {
		if entry.Platform != "android" {
			continue
		}
		release := entry.Release
		item := ClientUpdateAndroidEntry{
			Channel: entry.Channel, ID: release.ID, Version: release.Version, VersionCode: release.VersionCode,
			PackageName: release.PackageName, File: release.File, SHA256: release.SHA256, Size: release.Size,
			CertSHA256: release.CertSHA256, CanNotSkip: release.CanNotSkip, Notes: release.Notes, URL: release.URL,
			Disabled: release.Disabled,
		}
		if item.Notes == nil {
			item.Notes = map[string]string{}
		}
		if release.File != "" && !release.Disabled && cfg.Documents != nil {
			if ready, err := cfg.Documents.Ready(ctx, release.SHA256); err == nil {
				item.DocumentReady = &ready
			}
		}
		status.AndroidEntries = append(status.AndroidEntries, item)
	}
	return status, nil
}

// UploadClientUpdateRequest is the preflight of an uploaded package. SHA256
// and Size are filled from the staged bytes before the command is recorded,
// so the same command_id with different bytes is a COMMAND_ID_CONFLICT.
type UploadClientUpdateRequest struct {
	CommandMeta
	AllowDowngrade bool   `json:"allow_downgrade"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
}

// PublishClientUpdateRequest publishes a package staged by
// UploadClientUpdate. With DryRun it previews the publish again.
type PublishClientUpdateRequest struct {
	CommandMeta
	StagingID      string `json:"staging_id"`
	AllowDowngrade bool   `json:"allow_downgrade"`
	// Android 是发布 APK 时运营者填写的部分；暂存的是 APK 时必填，是桌面包时必须为空。
	Android *ClientUpdateAndroidRelease `json:"android,omitempty"`
}

// ClientUpdateAndroidRelease is what the operator fills in to publish an
// Android installer: everything else (version, package, signer) comes from the
// APK itself.
type ClientUpdateAndroidRelease struct {
	Channel    string            `json:"channel"`
	Notes      map[string]string `json:"notes"`
	CanNotSkip bool              `json:"can_not_skip"`
	URL        string            `json:"url,omitempty"`
}

// clientUpdateCatalog returns the in-process catalog, or the coded error the
// panel explains when the update service is disabled or external.
func (s *Service) clientUpdateCatalog() (ClientUpdateCatalog, error) {
	if s == nil || s.clientUpdates == nil {
		return nil, codedError(CodeUpdatesDisabled, errors.New("the client update service is not enabled (TELESRV_UPDATE_MANIFEST is not set)"))
	}
	if s.clientUpdates.Catalog == nil {
		return nil, codedError(CodeUpdatesExternal, errors.New("client updates are served by a separate telesrv-update; publish there"))
	}
	return s.clientUpdates.Catalog, nil
}

// UploadClientUpdate stages body and preflights it: a full verification and a
// dry-run publish that changes nothing. The result carries the staging id the
// confirm publishes, together with what would be published and replaced. The
// preflight is recorded as a dry-run command, failures included.
//
// The command store and metadata are checked before body is read; a staging
// failure is coded by its cause (see clientUpdateStagingError) and, since no
// command was begun yet, not recorded. The staged file is
// removed again when the preflight fails, when the upload replays an earlier
// command (whose result, and staging id, is returned instead) or conflicts
// with one, and when the package is already published.
func (s *Service) UploadClientUpdate(ctx context.Context, req UploadClientUpdateRequest, body io.Reader) (CommandResult, error) {
	catalog, err := s.clientUpdateCatalog()
	if err != nil {
		return CommandResult{}, err
	}
	// 先做不读请求体就能做的检查，再把可能 256 MiB 的上传收进暂存。
	if s.commands == nil {
		return CommandResult{}, errors.New("admin command store is not configured")
	}
	req.DryRun = true
	if req.CommandMeta, err = normalizeCommandMeta(req.CommandMeta); err != nil {
		return CommandResult{}, err
	}
	staged, err := catalog.StagePackage(body)
	if err != nil {
		return CommandResult{}, clientUpdateStagingError(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = catalog.DiscardStagedPackage(staged.ID)
		}
	}()
	req.SHA256, req.Size = staged.SHA256, staged.Size

	result, err := s.runCommand(ctx, req.CommandMeta, ActionPreflightClientUpdate, 0, domain.Peer{}, req, func() (CommandResult, error) {
		kind, err := catalog.StagedPackageKind(staged.ID)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		if kind == updatecdn.PackageKindAndroid {
			// APK 的通道与更新说明要在确认时填写：预检只解析包、核对身份，列出各通道当前的
			// 条目；版本回退在确认的试运行里按所选通道判断。
			inspected, err := catalog.InspectStagedAndroidPackage(staged.ID)
			if err != nil {
				return CommandResult{}, clientUpdateError(err)
			}
			details := androidInspectionDetails(inspected)
			details["staging_id"] = staged.ID
			keep = true
			return CommandResult{Message: "preflight passed; choose the channel and release notes to publish", Details: details}, nil
		}
		published, err := catalog.PublishStagedPackage(staged.ID, true, req.AllowDowngrade)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		details := clientUpdateDetails(published)
		details["kind"] = updatecdn.PackageKindDesktop
		if published.Unchanged {
			return CommandResult{Message: "package is already published; nothing to do", Details: details}, nil
		}
		details["staging_id"] = staged.ID
		keep = true
		return CommandResult{Message: "preflight passed", Details: details}, nil
	})
	if err != nil {
		// 例如预检通过后记录命令结果失败：不留下没有命令可以引用的暂存。
		keep = false
	}
	return result, err
}

// PublishClientUpdate publishes the staged package, verifying it again from
// scratch. A successful publish removes the staging; a failed one keeps it
// until it expires, so the operator can retry without uploading again.
func (s *Service) PublishClientUpdate(ctx context.Context, req PublishClientUpdateRequest) (CommandResult, error) {
	catalog, err := s.clientUpdateCatalog()
	if err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionPublishClientUpdate, 0, domain.Peer{}, req, func() (CommandResult, error) {
		kind, err := catalog.StagedPackageKind(req.StagingID)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		if kind == updatecdn.PackageKindAndroid {
			return s.publishAndroidClientUpdate(catalog, req)
		}
		if req.Android != nil {
			return CommandResult{}, codedError(CodeUpdateAndroidReleaseInvalid, errors.New("the staged package is a desktop package; android release fields do not apply"))
		}
		published, err := catalog.PublishStagedPackage(req.StagingID, req.DryRun, req.AllowDowngrade)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		details := clientUpdateDetails(published)
		details["kind"] = updatecdn.PackageKindDesktop
		switch {
		case req.DryRun:
			details["staging_id"] = req.StagingID
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		case published.Unchanged:
			return CommandResult{Message: "package is already published; nothing to do", Details: details}, nil
		}
		return CommandResult{Message: "published", Details: details}, nil
	})
}

// publishAndroidClientUpdate 是 PublishClientUpdate 的 APK 分支（在命令体里运行）。
func (s *Service) publishAndroidClientUpdate(catalog ClientUpdateCatalog, req PublishClientUpdateRequest) (CommandResult, error) {
	if req.Android == nil {
		return CommandResult{}, codedError(CodeUpdateAndroidReleaseInvalid, errors.New("the staged package is an Android installer: channel and release notes are required"))
	}
	meta := updatecdn.AndroidReleaseMeta{
		Channel:    req.Android.Channel,
		Notes:      req.Android.Notes,
		CanNotSkip: req.Android.CanNotSkip,
		URL:        req.Android.URL,
	}
	published, err := catalog.PublishStagedAndroidPackage(req.StagingID, meta, req.DryRun, req.AllowDowngrade)
	if err != nil {
		return CommandResult{}, clientUpdateError(err)
	}
	details := androidPublishDetails(published)
	switch {
	case req.DryRun:
		details["staging_id"] = req.StagingID
		return CommandResult{Message: "dry-run completed", Details: details}, nil
	case published.Unchanged:
		return CommandResult{Message: "package is already published; nothing to do", Details: details}, nil
	}
	return CommandResult{Message: "published; Android clients see it once the package is registered as a document", Details: details}, nil
}

// androidInspectionDetails 是 APK 预检记录的结果：从包里读出的元数据，与各通道当前的条目。
func androidInspectionDetails(r *updatecdn.AndroidInspection) map[string]any {
	current := make(map[string]any, len(r.Current))
	for channel, release := range r.Current {
		current[channel] = map[string]any{
			"version":      release.Version,
			"version_code": release.VersionCode,
			"file":         release.File,
			"disabled":     release.Disabled,
		}
	}
	return map[string]any{
		"kind":         updatecdn.PackageKindAndroid,
		"package_name": r.APK.PackageName,
		"version":      r.APK.VersionName,
		"version_code": r.APK.VersionCode,
		"min_sdk":      r.APK.MinSDK,
		"cert_sha256":  r.APK.SignerCertSHA256,
		"signing":      r.APK.SigningScheme,
		"size":         r.Size,
		"sha256":       r.SHA256,
		"current":      current,
	}
}

// androidPublishDetails 是 APK 发布（或试运行）记录的结果。更新说明只记语言，正文在命令请求里。
func androidPublishDetails(r *updatecdn.AndroidPublishResult) map[string]any {
	langs := make([]string, 0, len(r.Release.Notes))
	for lang := range r.Release.Notes {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	details := map[string]any{
		"kind":             updatecdn.PackageKindAndroid,
		"platform":         "android",
		"channel":          r.Channel,
		"version":          r.Release.Version,
		"version_code":     r.Release.VersionCode,
		"package_name":     r.Release.PackageName,
		"file":             r.Release.File,
		"size":             r.Release.Size,
		"sha256":           r.Release.SHA256,
		"cert_sha256":      r.Release.CertSHA256,
		"can_not_skip":     r.Release.CanNotSkip,
		"notes_languages":  langs,
		"url":              r.Release.URL,
		"manifest_updated": r.ManifestUpdated,
		"file_copied":      r.FileCopied,
		"unchanged":        r.Unchanged,
		"downgrade":        r.Downgrade,
	}
	if p := r.Previous; p != nil {
		details["previous"] = map[string]any{
			"version":      p.Version,
			"version_code": p.VersionCode,
			"file":         p.File,
			"sha256":       p.SHA256,
			"disabled":     p.Disabled,
		}
	}
	return details
}

// ClientUpdateEntryRequest names the desktop.<platform>.<channel> entry to
// disable or re-enable.
type ClientUpdateEntryRequest struct {
	CommandMeta
	Platform string `json:"platform"`
	Channel  string `json:"channel"`
}

// DisableClientUpdate stops serving one desktop entry right away. The entry
// stays in the manifest marked disabled, so it can be re-enabled and a later
// publish still treats its build as published. Disabling a disabled entry is
// reported unchanged and writes nothing.
func (s *Service) DisableClientUpdate(ctx context.Context, req ClientUpdateEntryRequest) (CommandResult, error) {
	return s.setClientUpdateDisabled(ctx, req, ActionDisableClientUpdate, true)
}

// EnableClientUpdate serves a disabled desktop entry again. It is a deliberate
// operator decision on what may be a pulled bad build, so it skips the
// downgrade check a publish makes; the package must still be present and
// still verify. Like every command it needs a reason.
func (s *Service) EnableClientUpdate(ctx context.Context, req ClientUpdateEntryRequest) (CommandResult, error) {
	return s.setClientUpdateDisabled(ctx, req, ActionEnableClientUpdate, false)
}

func (s *Service) setClientUpdateDisabled(ctx context.Context, req ClientUpdateEntryRequest, action string, disabled bool) (CommandResult, error) {
	catalog, err := s.clientUpdateCatalog()
	if err != nil {
		return CommandResult{}, err
	}
	req.Platform = strings.TrimSpace(req.Platform)
	req.Channel = strings.TrimSpace(req.Channel)
	if req.Platform == "" || req.Channel == "" {
		return CommandResult{}, codedError(CodeUpdateEntryInvalid, errors.New("platform and channel are required"))
	}
	if req.Platform == "android" {
		return s.runCommand(ctx, req.CommandMeta, action, 0, domain.Peer{}, req, func() (CommandResult, error) {
			changed, err := catalog.SetAppReleaseDisabled(req.Platform, req.Channel, disabled, req.DryRun)
			if err != nil {
				return CommandResult{}, clientUpdateError(err)
			}
			release := changed.Release
			details := map[string]any{
				"platform":     changed.Platform,
				"channel":      changed.Channel,
				"version":      release.Version,
				"version_code": release.VersionCode,
				"file":         release.File,
				"sha256":       release.SHA256,
				"size":         release.Size,
				"disabled":     release.Disabled,
				"unchanged":    changed.Unchanged,
			}
			return CommandResult{Message: entryToggleMessage(changed.Unchanged, disabled, req.DryRun), Details: details}, nil
		})
	}
	return s.runCommand(ctx, req.CommandMeta, action, 0, domain.Peer{}, req, func() (CommandResult, error) {
		changed, err := catalog.SetDesktopReleaseDisabled(req.Platform, req.Channel, disabled, req.DryRun)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		release := changed.Release
		details := map[string]any{
			"platform":  changed.Platform,
			"channel":   changed.Channel,
			"build":     release.Build,
			"version":   release.Version,
			"file":      release.File,
			"sha256":    release.SHA256,
			"size":      release.Size,
			"disabled":  release.Disabled,
			"unchanged": changed.Unchanged,
		}
		return CommandResult{Message: entryToggleMessage(changed.Unchanged, disabled, req.DryRun), Details: details}, nil
	})
}

func entryToggleMessage(unchanged, disabled, dryRun bool) string {
	switch {
	case unchanged && disabled:
		return "entry is already disabled; nothing to do"
	case unchanged:
		return "entry is already enabled; nothing to do"
	case dryRun:
		return "dry-run completed"
	case disabled:
		return "disabled"
	}
	return "re-enabled"
}

// clientUpdateStagingError codes a failure to stage an upload by whose problem
// it is: the package (too large), the pending uploads (full), the client's
// transfer (read failed: retry the upload) or the server's storage (disk full,
// directory not writable: an operator has to look). An unclassified failure is
// counted as the server's.
func clientUpdateStagingError(err error) error {
	switch {
	case errors.Is(err, updatecdn.ErrStagedPackageTooLarge):
		return codedError(CodeUpdatePackageTooLarge, err)
	case errors.Is(err, updatecdn.ErrStagingFull):
		return codedError(CodeUpdateStagingFull, err)
	case errors.Is(err, updatecdn.ErrStagingReadFailed):
		return codedError(CodeUpdateUploadFailed, err)
	}
	return codedError(CodeUpdateStagingStorage, err)
}

// clientUpdateError attaches the stable code the panel switches on.
func clientUpdateError(err error) error {
	switch {
	case errors.Is(err, updatecdn.ErrStagingNotFound):
		return codedError(CodeUpdateStagingNotFound, errors.New("the uploaded package is no longer staged; upload it again"))
	case errors.Is(err, updatecdn.ErrStagingExpired):
		return codedError(CodeUpdateStagingExpired, errors.New("the uploaded package expired; upload it again"))
	case errors.Is(err, updatecdn.ErrPublishInProgress):
		return codedError(CodeUpdatePublishInProgress, err)
	case errors.Is(err, updatecdn.ErrStagedPackageTooLarge):
		return codedError(CodeUpdatePackageTooLarge, err)
	case errors.Is(err, updatecdn.ErrDesktopEntryNotFound):
		return codedError(CodeUpdateEntryNotFound, err)
	case errors.Is(err, updatecdn.ErrAndroidIdentityMismatch):
		return codedError(CodeUpdateAndroidIdentityMismatch, err)
	case errors.Is(err, updatecdn.ErrAndroidReleaseInvalid):
		return codedError(CodeUpdateAndroidReleaseInvalid, err)
	}
	return codedError(CodeUpdatePackageRejected, err)
}

// clientUpdateDetails is the recorded outcome of a preflight or publish: the
// entry that is (or would be) written, and the entry it replaces.
func clientUpdateDetails(r *updatecdn.PublishResult) map[string]any {
	details := map[string]any{
		"platform":         r.Platform,
		"channel":          r.Channel,
		"build":            r.Release.Build,
		"file":             r.Release.File,
		"size":             r.Release.Size,
		"sha256":           r.Release.SHA256,
		"manifest_updated": r.ManifestUpdated,
		"file_copied":      r.FileCopied,
		"unchanged":        r.Unchanged,
		// downgrade：被替换的条目 build 不小于新包（回滚，或用同一 build 重新启用被停用的
		// 条目），只有 allow_downgrade 才能发布。
		"downgrade": r.Previous != nil && r.ManifestUpdated && r.Release.Build <= r.Previous.Build,
	}
	if !r.Package.Created.IsZero() {
		details["created"] = r.Package.Created.UTC().Format(time.RFC3339)
	}
	if r.Previous != nil {
		details["previous"] = map[string]any{
			"build":    r.Previous.Build,
			"file":     r.Previous.File,
			"sha256":   r.Previous.SHA256,
			"size":     r.Previous.Size,
			"disabled": r.Previous.Disabled,
		}
	}
	return details
}
