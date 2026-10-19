// Package updatecdn implements the release catalog shared by the update HTTP
// service and telesrv's help.getAppUpdate resolver client.
package updatecdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ManifestSchemaVersion = 1

var (
	sha256RE  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	versionRE = regexp.MustCompile(`\d+(?:\.\d+)*`)
	// desktopFileRE 是 v2 客户端查找更新包所用文件名的服务端子集：客户端会把下载
	// URL 里的文件名过滤成只剩 [a-zA-Z0-9_-] 再按此格式匹配，不匹配就静默当作
	// 没有更新。服务端不提供 canary 通道，因此只允许可选的 -beta 后缀。
	desktopFileRE = regexp.MustCompile(`^td-update-(win|mac|linux)-(x86|x64|arm)-([0-9]+)(-beta)?$`)
)

// Manifest is the operator-managed update catalog. Desktop entries feed the
// native TDesktop /current4 protocol; Apps entries feed help.getAppUpdate.
type Manifest struct {
	SchemaVersion int                                      `json:"schema_version"`
	Desktop       map[string]map[string]DesktopRelease     `json:"desktop,omitempty"`
	Apps          map[string]map[string]ApplicationRelease `json:"apps,omitempty"`
}

// DesktopRelease points at a package produced by TDesktop's Packer target.
// The package has its own client-verified RSA signature; SHA256 additionally
// prevents publishing a truncated or accidentally replaced file.
type DesktopRelease struct {
	Build    uint64 `json:"build"`
	Version  string `json:"version,omitempty"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// ApplicationRelease is returned through help.appUpdate for mobile and other
// clients that use the MTProto update mechanism.
type ApplicationRelease struct {
	ID          int               `json:"id"`
	Version     string            `json:"version"`
	URL         string            `json:"url,omitempty"`
	URLBySource map[string]string `json:"url_by_source,omitempty"`
	CanNotSkip  bool              `json:"can_not_skip,omitempty"`
	Notes       map[string]string `json:"notes"`
	Disabled    bool              `json:"disabled,omitempty"`
}

// ResolveRequest describes a help.getAppUpdate client.
type ResolveRequest struct {
	Platform string
	Channel  string
	Version  string
	Source   string
	LangCode string
}

// ResolvedUpdate is the transport-neutral help.appUpdate payload.
type ResolvedUpdate struct {
	ID         int    `json:"id"`
	Version    string `json:"version"`
	Text       string `json:"text"`
	URL        string `json:"url,omitempty"`
	CanNotSkip bool   `json:"can_not_skip,omitempty"`
}

type fileRecord struct {
	path    string
	name    string
	sha256  string
	size    int64
	modTime time.Time
}

// Catalog is an immutable, validated manifest snapshot.
type Catalog struct {
	manifest Manifest
	files    map[string]fileRecord
}

// LoadCatalog parses and fully validates a manifest and all enabled desktop
// packages. Unknown JSON fields fail closed so operator typos cannot silently
// publish an incomplete update.
func LoadCatalog(manifestPath, filesDir string) (*Catalog, error) {
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("stat manifest: %w", err)
	} else if info.Size() > 4<<20 {
		return nil, fmt.Errorf("manifest exceeds 4 MiB")
	}

	var manifest Manifest
	decoder := json.NewDecoder(io.LimitReader(f, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return nil, fmt.Errorf("schema_version = %d, want %d", manifest.SchemaVersion, ManifestSchemaVersion)
	}

	catalog := &Catalog{manifest: manifest, files: make(map[string]fileRecord)}
	if err := catalog.validateDesktop(filesDir); err != nil {
		return nil, err
	}
	if err := catalog.validateApps(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode manifest trailer: %w", err)
	}
	return fmt.Errorf("manifest contains more than one JSON value")
}

func (c *Catalog) validateDesktop(filesDir string) error {
	for platform, channels := range c.manifest.Desktop {
		if !validDesktopPlatform(platform) {
			return fmt.Errorf("desktop platform %q is unsupported", platform)
		}
		for channel, release := range channels {
			prefix := fmt.Sprintf("desktop.%s.%s", platform, channel)
			if err := validateDesktopChannel(channel); err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
			if release.Disabled {
				continue
			}
			if release.Build == 0 {
				return fmt.Errorf("%s.build must be positive", prefix)
			}
			if release.File == "" || filepath.Base(release.File) != release.File || strings.ContainsAny(release.File, `/\\`) {
				return fmt.Errorf("%s.file must be a single file name", prefix)
			}
			if err := validateDesktopFileName(platform, channel, release); err != nil {
				return fmt.Errorf("%s.file %q %w", prefix, release.File, err)
			}
			if !sha256RE.MatchString(release.SHA256) {
				return fmt.Errorf("%s.sha256 must contain 64 hexadecimal characters", prefix)
			}
			if existing, ok := c.files[release.File]; ok {
				if !strings.EqualFold(existing.sha256, release.SHA256) {
					return fmt.Errorf("%s.file %q is reused with another SHA256", prefix, release.File)
				}
				continue
			}

			path := filepath.Join(filesDir, release.File)
			record, err := verifyDesktopFile(path, release)
			if err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
			c.files[release.File] = record
		}
	}
	return nil
}

// validateDesktopFileName 检查文件名符合 v2 客户端约定 td-update-{os}-{arch}-{base}[-beta]，
// 且 {os}-{arch} 与平台键对应、{base} 等于 build、-beta 后缀当且仅当通道为 beta。
func validateDesktopFileName(platform, channel string, release DesktopRelease) error {
	match := desktopFileRE.FindStringSubmatch(release.File)
	if match == nil {
		return fmt.Errorf("must match td-update-{os}-{arch}-{base}[-beta]")
	}
	target := match[1] + "-" + match[2]
	if want := desktopPlatformTargets[platform]; target != want {
		return fmt.Errorf("targets %s, want %s for platform %s", target, want, platform)
	}
	// 按字符串比较，前导零之类的写法与 /current4 宣告的 released 对不上，一并拒绝。
	if base := match[3]; base != strconv.FormatUint(release.Build, 10) {
		return fmt.Errorf("base %s must equal build %d", base, release.Build)
	}
	if hasBeta := match[4] != ""; hasBeta != (channel == "beta") {
		if hasBeta {
			return fmt.Errorf("must not have the -beta suffix on the %s channel", channel)
		}
		return fmt.Errorf("must have the -beta suffix on the beta channel")
	}
	return nil
}

func verifyDesktopFile(path string, release DesktopRelease) (fileRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileRecord{}, fmt.Errorf("open package %q: %w", release.File, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fileRecord{}, fmt.Errorf("stat package %q: %w", release.File, err)
	}
	if !info.Mode().IsRegular() {
		return fileRecord{}, fmt.Errorf("package %q is not a regular file", release.File)
	}
	if release.Size > 0 && release.Size != info.Size() {
		return fileRecord{}, fmt.Errorf("package %q size = %d, want %d", release.File, info.Size(), release.Size)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fileRecord{}, fmt.Errorf("hash package %q: %w", release.File, err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, release.SHA256) {
		return fileRecord{}, fmt.Errorf("package %q SHA256 = %s, want %s", release.File, actual, strings.ToLower(release.SHA256))
	}
	return fileRecord{
		path:    path,
		name:    release.File,
		sha256:  actual,
		size:    info.Size(),
		modTime: info.ModTime(),
	}, nil
}

func (c *Catalog) validateApps() error {
	for platform, channels := range c.manifest.Apps {
		if !validAppPlatform(platform) {
			return fmt.Errorf("apps platform %q is unsupported", platform)
		}
		for channel, release := range channels {
			if !validAppChannel(channel) {
				return fmt.Errorf("apps.%s channel %q is unsupported", platform, channel)
			}
			if release.Disabled {
				continue
			}
			prefix := fmt.Sprintf("apps.%s.%s", platform, channel)
			if release.ID <= 0 {
				return fmt.Errorf("%s.id must be positive", prefix)
			}
			if _, ok := parseVersion(release.Version); !ok {
				return fmt.Errorf("%s.version must contain a numeric version", prefix)
			}
			if len(release.Notes) == 0 {
				return fmt.Errorf("%s.notes must contain at least one localization", prefix)
			}
			for lang, note := range release.Notes {
				if strings.TrimSpace(lang) == "" || strings.TrimSpace(note) == "" {
					return fmt.Errorf("%s.notes contains an empty language or text", prefix)
				}
			}
			if release.URL != "" {
				if err := validateDownloadURL(release.URL); err != nil {
					return fmt.Errorf("%s.url: %w", prefix, err)
				}
			}
			for source, rawURL := range release.URLBySource {
				if strings.TrimSpace(source) == "" {
					return fmt.Errorf("%s.url_by_source contains an empty source", prefix)
				}
				if err := validateDownloadURL(rawURL); err != nil {
					return fmt.Errorf("%s.url_by_source[%q]: %w", prefix, source, err)
				}
			}
		}
	}
	return nil
}

func validateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("must be an HTTP(S) URL without credentials")
	}
	return nil
}

// desktopPlatformTargets 把 /current4 的平台键映射到 v2 包文件名中的 {os}-{arch}。
var desktopPlatformTargets = map[string]string{
	"win":    "win-x86",
	"win64":  "win-x64",
	"winarm": "win-arm",
	"mac":    "mac-x64",
	"armac":  "mac-arm",
	"linux":  "linux-x64",
}

func validDesktopPlatform(platform string) bool {
	_, ok := desktopPlatformTargets[platform]
	return ok
}

func validAppPlatform(platform string) bool {
	switch platform {
	case "android", "ios", "macos", "tdesktop":
		return true
	default:
		return false
	}
}

// validAppChannel 是移动端 apps 条目的通道集合。
func validAppChannel(channel string) bool {
	return channel == "stable" || channel == "beta" || channel == "alpha"
}

// validateDesktopChannel 只接受 v2 桌面客户端会请求的 stable 与 beta；
// alpha 是 v1 的通道，v2 客户端不再识别，单独给出说明。
func validateDesktopChannel(channel string) error {
	switch channel {
	case "stable", "beta":
		return nil
	case "alpha":
		return fmt.Errorf("channel %q is unsupported: Telegram Desktop v2 updates only use stable and beta", channel)
	default:
		return fmt.Errorf("channel %q is unsupported", channel)
	}
}

// DesktopMap returns the exact JSON object consumed by TDesktop's current4
// parser. Links are deliberately relative to autoupdate_url_prefix.
func (c *Catalog) DesktopMap() map[string]map[string]map[string]any {
	result := make(map[string]map[string]map[string]any)
	for platform, channels := range c.manifest.Desktop {
		published := make(map[string]map[string]any)
		for channel, release := range channels {
			if release.Disabled {
				continue
			}
			published[channel] = map[string]any{
				"released": release.Build,
				"link":     "/files/" + url.PathEscape(release.File),
			}
		}
		if len(published) != 0 {
			result[platform] = published
		}
	}
	return result
}

// Resolve returns a release only when it is newer than the supplied client
// version. Empty channel means stable.
func (c *Catalog) Resolve(req ResolveRequest) (*ResolvedUpdate, error) {
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	if channel == "" {
		channel = "stable"
	}
	channels, ok := c.manifest.Apps[platform]
	if !ok {
		return nil, nil
	}
	release, ok := channels[channel]
	if !ok || release.Disabled {
		return nil, nil
	}
	if compareVersions(req.Version, release.Version) >= 0 {
		return nil, nil
	}

	text := localizedText(release.Notes, req.LangCode)
	if text == "" {
		return nil, fmt.Errorf("release %s/%s has no usable localized text", platform, channel)
	}
	updateURL := release.URL
	if specific := release.URLBySource[req.Source]; specific != "" {
		updateURL = specific
	}
	return &ResolvedUpdate{
		ID:         release.ID,
		Version:    release.Version,
		Text:       text,
		URL:        updateURL,
		CanNotSkip: release.CanNotSkip,
	}, nil
}

func localizedText(values map[string]string, langCode string) string {
	if len(values) == 0 {
		return ""
	}
	normalizedValues := make(map[string]string, len(values))
	for key, value := range values {
		normalizedValues[strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "_", "-"))] = value
	}
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(langCode), "_", "-"))
	if text := normalizedValues[normalized]; text != "" {
		return text
	}
	if base, _, ok := strings.Cut(normalized, "-"); ok {
		if text := normalizedValues[base]; text != "" {
			return text
		}
	}
	if text := normalizedValues["en"]; text != "" {
		return text
	}
	keys := make([]string, 0, len(normalizedValues))
	for key := range normalizedValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return normalizedValues[keys[0]]
}

func compareVersions(left, right string) int {
	a, aOK := parseVersion(left)
	b, bOK := parseVersion(right)
	if !aOK && !bOK {
		return strings.Compare(strings.TrimSpace(left), strings.TrimSpace(right))
	}
	if !aOK {
		return -1
	}
	if !bOK {
		return 1
	}
	max := len(a)
	if len(b) > max {
		max = len(b)
	}
	for i := 0; i < max; i++ {
		var av, bv uint64
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func parseVersion(value string) ([]uint64, bool) {
	match := versionRE.FindString(value)
	if match == "" {
		return nil, false
	}
	rawParts := strings.Split(match, ".")
	parts := make([]uint64, 0, len(rawParts))
	for _, raw := range rawParts {
		part, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
	}
	return parts, true
}

func (c *Catalog) file(name string) (fileRecord, bool) {
	record, ok := c.files[name]
	return record, ok
}
