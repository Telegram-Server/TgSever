package admin

import (
	"context"
	"time"

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

// ClientUpdateCatalog is the in-process update catalog. *updatecdn.Store
// implements it: Status lists the served entries and the reload error, and
// the manifest path, files directory and load options are what a publish
// from the panel validates against, exactly as the store reloads.
type ClientUpdateCatalog interface {
	Status() (updatecdn.StoreStatus, error)
	ManifestPath() string
	FilesDir() string
	CatalogOptions() updatecdn.CatalogOptions
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
