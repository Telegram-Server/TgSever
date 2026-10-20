package admin

import (
	"context"
	"errors"
	"io"
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
)

// 客户端更新写操作的稳定错误码，面板按它显示说明而不是匹配英文错误。
const (
	CodeUpdatesDisabled         = "UPDATES_DISABLED"
	CodeUpdatesExternal         = "UPDATES_EXTERNAL"
	CodeUpdateStagingNotFound   = "UPDATE_STAGING_NOT_FOUND"
	CodeUpdateStagingExpired    = "UPDATE_STAGING_EXPIRED"
	CodeUpdatePublishInProgress = "UPDATE_PUBLISH_IN_PROGRESS"
	CodeUpdatePackageTooLarge   = "UPDATE_PACKAGE_TOO_LARGE"
	// CodeUpdatePackageRejected 是预检或发布的其余失败：签名、平台、通道、版本回退、
	// 同名文件内容不同、新清单无效。具体原因在错误文本里。
	CodeUpdatePackageRejected = "UPDATE_PACKAGE_REJECTED"
)

// ClientUpdateCatalog is the in-process update catalog. *updatecdn.Store
// implements it: Status lists the served entries and the reload error, and
// the manifest path, files directory and load options are what a publish
// from the panel validates against, exactly as the store reloads. The staging
// methods stage an upload next to the manifest, preview or publish it with
// the store's own options, and drop it (see updatecdn.StagePackage).
type ClientUpdateCatalog interface {
	Status() (updatecdn.StoreStatus, error)
	ManifestPath() string
	FilesDir() string
	CatalogOptions() updatecdn.CatalogOptions
	StagePackage(r io.Reader) (updatecdn.StagedPackage, error)
	PublishStagedPackage(id string, dryRun, allowDowngrade bool) (*updatecdn.PublishResult, error)
	DiscardStagedPackage(id string) error
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
}

// ClientUpdateStatus is what the panel's client updates page shows.
type ClientUpdateStatus struct {
	Mode       string `json:"mode"`
	PublicURL  string `json:"public_url,omitempty"`
	ServiceURL string `json:"service_url,omitempty"`
	// ReloadError 非空时清单最近一次重新加载失败，Entries 来自上一份正常清单。
	ReloadError string              `json:"reload_error,omitempty"`
	Entries     []ClientUpdateEntry `json:"entries"`
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
	status := ClientUpdateStatus{Mode: ClientUpdatesDisabled, Entries: []ClientUpdateEntry{}}
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
// The command metadata is checked before body is read. The staged file is
// removed again when the preflight fails, when the upload replays an earlier
// command (whose result, and staging id, is returned instead) or conflicts
// with one, and when the package is already published.
func (s *Service) UploadClientUpdate(ctx context.Context, req UploadClientUpdateRequest, body io.Reader) (CommandResult, error) {
	catalog, err := s.clientUpdateCatalog()
	if err != nil {
		return CommandResult{}, err
	}
	req.DryRun = true
	if req.CommandMeta, err = normalizeCommandMeta(req.CommandMeta); err != nil {
		return CommandResult{}, err
	}
	staged, err := catalog.StagePackage(body)
	if err != nil {
		return CommandResult{}, clientUpdateError(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = catalog.DiscardStagedPackage(staged.ID)
		}
	}()
	req.SHA256, req.Size = staged.SHA256, staged.Size

	result, err := s.runCommand(ctx, req.CommandMeta, ActionPreflightClientUpdate, 0, domain.Peer{}, req, func() (CommandResult, error) {
		published, err := catalog.PublishStagedPackage(staged.ID, true, req.AllowDowngrade)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		details := clientUpdateDetails(published)
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
		published, err := catalog.PublishStagedPackage(req.StagingID, req.DryRun, req.AllowDowngrade)
		if err != nil {
			return CommandResult{}, clientUpdateError(err)
		}
		details := clientUpdateDetails(published)
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
