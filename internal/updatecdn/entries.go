package updatecdn

import (
	"sort"
	"time"
)

// DesktopEntry 是清单中的一个桌面条目（desktop.<Platform>.<Channel>），供管理后台
// 列出当前发布的版本。与 DesktopMap 不同，停用的条目也会列出。
type DesktopEntry struct {
	Platform string
	Channel  string
	// Release 是清单中的条目。启用条目的 Size 取自已校验的包文件（手写清单可能
	// 省略 size）；停用条目只有清单里的字段。
	Release DesktopRelease
	// Created 是 Packer 写入包中的打包时间。停用条目的包加载时不做校验，为零值。
	Created time.Time
}

// DesktopReleases returns every desktop entry of the manifest, disabled ones
// included, sorted by platform and channel.
func (c *Catalog) DesktopReleases() []DesktopEntry {
	var entries []DesktopEntry
	for platform, channels := range c.manifest.Desktop {
		for channel, release := range channels {
			entry := DesktopEntry{Platform: platform, Channel: channel, Release: release}
			if !release.Disabled {
				if record, ok := c.files[release.File]; ok {
					entry.Release.Size = record.size
					entry.Created = record.pkg.Created
				}
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Platform != entries[j].Platform {
			return entries[i].Platform < entries[j].Platform
		}
		return entries[i].Channel < entries[j].Channel
	})
	return entries
}

// StoreStatus 是 Store 某一时刻的状态：正在提供的快照中的全部桌面条目（含停用，
// 见 Catalog.DesktopReleases），以及清单最近一次重新加载失败的原因（为 nil 表示
// 快照就是当前清单）。两者在同一把锁下取得。
type StoreStatus struct {
	Desktop   []DesktopEntry
	ReloadErr error
}

// Status reloads the catalog when the manifest changed, like Snapshot, and
// reports the served snapshot together with why the latest manifest could not
// be loaded, if it could not.
func (s *Store) Status() (StoreStatus, error) {
	catalog, stale, err := s.snapshot()
	if err != nil {
		return StoreStatus{}, err
	}
	return StoreStatus{Desktop: catalog.DesktopReleases(), ReloadErr: stale}, nil
}

// ManifestPath returns the manifest the store loads.
func (s *Store) ManifestPath() string { return s.manifestPath }

// FilesDir returns the directory holding the published packages.
func (s *Store) FilesDir() string { return s.filesDir }

// CatalogOptions returns the options every load uses, including the update
// root public key, so a publish can validate exactly as the store reloads.
func (s *Store) CatalogOptions() CatalogOptions { return s.opts }
