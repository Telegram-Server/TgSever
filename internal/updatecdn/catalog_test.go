package updatecdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogDesktopMapAndResolve(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageData := []byte("signed-tdesktop-update-package")
	packageName := "td-update-win-x64-7007000"
	if err := os.WriteFile(filepath.Join(filesDir, packageName), packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(packageData)
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {
				Build: 7007000, Version: "7.0.7", File: packageName,
				SHA256: hex.EncodeToString(hash[:]), Size: int64(len(packageData)),
			}},
		},
		Apps: map[string]map[string]ApplicationRelease{
			"android": {"stable": {
				ID: 77, Version: "12.9.1", URL: "https://updates.example/app.apk",
				URLBySource: map[string]string{"com.example.store": "https://store.example/app"},
				Notes:       map[string]string{"en": "New version", "ru": "Новая версия"},
			}},
		},
	}
	manifestPath := writeTestManifest(t, dir, manifest)
	catalog, err := LoadCatalog(manifestPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}

	entry := catalog.DesktopMap()["win64"]["stable"]
	if entry["released"] != uint64(7007000) || entry["link"] != "/files/td-update-win-x64-7007000" {
		t.Fatalf("desktop entry = %#v", entry)
	}
	resolved, err := catalog.Resolve(ResolveRequest{
		Platform: "android", Version: "12.9.0 (500)", Source: "com.example.store", LangCode: "ru-RU",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.ID != 77 || resolved.Text != "Новая версия" || resolved.URL != "https://store.example/app" {
		t.Fatalf("resolved update = %#v", resolved)
	}
	current, err := catalog.Resolve(ResolveRequest{Platform: "android", Version: "12.9.1", LangCode: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if current != nil {
		t.Fatalf("current client got update %#v", current)
	}
}

func TestLoadCatalogRejectsPackageHashMismatch(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "td-update-win-x64-7007000"), []byte("package"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {Build: 7007000, File: "td-update-win-x64-7007000", SHA256: strings.Repeat("0", 64)}},
		},
	})
	if _, err := LoadCatalog(manifestPath, filesDir); err == nil {
		t.Fatal("hash mismatch accepted")
	}
}

func writeTestManifest(t *testing.T, dir string, manifest Manifest) string {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadDesktopEntry 写入只含单个桌面条目的清单和对应包文件，返回 LoadCatalog 的结果。
// 启用条目的 SHA256 与大小按写入的包内容自动填写。
func loadDesktopEntry(t *testing.T, platform, channel string, release DesktopRelease) error {
	t.Helper()
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !release.Disabled {
		data := []byte("package-" + release.File)
		if err := os.WriteFile(filepath.Join(filesDir, release.File), data, 0o600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		release.SHA256 = hex.EncodeToString(hash[:])
		release.Size = int64(len(data))
	}
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop:       map[string]map[string]DesktopRelease{platform: {channel: release}},
	})
	_, err := LoadCatalog(manifestPath, filesDir)
	return err
}

func assertLoadError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("LoadCatalog() error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("LoadCatalog() error = nil, want %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadCatalog() error = %q, want it to contain %q", err, want)
	}
}

func TestLoadCatalogDesktopChannels(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		file    string
		wantErr string
	}{
		{name: "stable", channel: "stable", file: "td-update-win-x64-7000007"},
		{name: "beta", channel: "beta", file: "td-update-win-x64-7000007-beta"},
		{name: "alpha rejected", channel: "alpha", file: "td-update-win-x64-7000007",
			wantErr: `desktop.win64.alpha: channel "alpha" is unsupported: Telegram Desktop v2 updates only use stable and beta`},
		{name: "unknown rejected", channel: "dev", file: "td-update-win-x64-7000007",
			wantErr: `desktop.win64.dev: channel "dev" is unsupported`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadDesktopEntry(t, "win64", tt.channel, DesktopRelease{Build: 7000007, File: tt.file})
			assertLoadError(t, err, tt.wantErr)
		})
	}
}

func TestLoadCatalogDesktopFileNameFormat(t *testing.T) {
	const formatErr = "must match td-update-{os}-{arch}-{base}[-beta]"
	tests := []struct {
		name    string
		file    string
		wantErr string
	}{
		{name: "v2 name", file: "td-update-win-x64-7000007"},
		{name: "v1 name rejected", file: "tx64upd7000007", wantErr: `desktop.win64.stable.file "tx64upd7000007" ` + formatErr},
		{name: "non-numeric base rejected", file: "td-update-win-x64-7.0.7", wantErr: formatErr},
		{name: "missing base rejected", file: "td-update-win-x64", wantErr: formatErr},
		{name: "canary suffix rejected", file: "td-update-win-x64-7000007-canary-1", wantErr: formatErr},
		{name: "extension rejected", file: "td-update-win-x64-7000007.zip", wantErr: formatErr},
		{name: "upper case rejected", file: "TD-UPDATE-WIN-X64-7000007", wantErr: formatErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadDesktopEntry(t, "win64", "stable", DesktopRelease{Build: 7000007, File: tt.file})
			assertLoadError(t, err, tt.wantErr)
		})
	}
}

func TestLoadCatalogDesktopFileNameMatchesPlatform(t *testing.T) {
	tests := []struct {
		platform string
		file     string
		wantErr  string
	}{
		{platform: "win", file: "td-update-win-x86-7000007"},
		{platform: "win64", file: "td-update-win-x64-7000007"},
		{platform: "winarm", file: "td-update-win-arm-7000007"},
		{platform: "mac", file: "td-update-mac-x64-7000007"},
		{platform: "armac", file: "td-update-mac-arm-7000007"},
		{platform: "linux", file: "td-update-linux-x64-7000007"},
		{platform: "win64", file: "td-update-win-x86-7000007",
			wantErr: `desktop.win64.stable.file "td-update-win-x86-7000007" targets win-x86, want win-x64 for platform win64`},
		{platform: "win", file: "td-update-win-x64-7000007", wantErr: "want win-x86 for platform win"},
		{platform: "armac", file: "td-update-mac-x64-7000007", wantErr: "want mac-arm for platform armac"},
		{platform: "mac", file: "td-update-linux-x64-7000007", wantErr: "want mac-x64 for platform mac"},
		{platform: "linux", file: "td-update-linux-arm-7000007", wantErr: "want linux-x64 for platform linux"},
		{platform: "winarm", file: "td-update-mac-arm-7000007", wantErr: "want win-arm for platform winarm"},
	}
	for _, tt := range tests {
		t.Run(tt.platform+"/"+tt.file, func(t *testing.T) {
			err := loadDesktopEntry(t, tt.platform, "stable", DesktopRelease{Build: 7000007, File: tt.file})
			assertLoadError(t, err, tt.wantErr)
		})
	}
}

func TestLoadCatalogDesktopFileNameSuffixMatchesChannel(t *testing.T) {
	tests := []struct {
		channel string
		file    string
		wantErr string
	}{
		{channel: "stable", file: "td-update-linux-x64-7000007"},
		{channel: "beta", file: "td-update-linux-x64-7000007-beta"},
		{channel: "stable", file: "td-update-linux-x64-7000007-beta",
			wantErr: `desktop.linux.stable.file "td-update-linux-x64-7000007-beta" must not have the -beta suffix on the stable channel`},
		{channel: "beta", file: "td-update-linux-x64-7000007",
			wantErr: `desktop.linux.beta.file "td-update-linux-x64-7000007" must have the -beta suffix on the beta channel`},
	}
	for _, tt := range tests {
		t.Run(tt.channel+"/"+tt.file, func(t *testing.T) {
			err := loadDesktopEntry(t, "linux", tt.channel, DesktopRelease{Build: 7000007, File: tt.file})
			assertLoadError(t, err, tt.wantErr)
		})
	}
}

func TestLoadCatalogDesktopFileNameBaseMatchesBuild(t *testing.T) {
	tests := []struct {
		name    string
		build   uint64
		file    string
		wantErr string
	}{
		{name: "equal", build: 7000007, file: "td-update-mac-arm-7000007"},
		{name: "different build rejected", build: 7000008, file: "td-update-mac-arm-7000007",
			wantErr: `desktop.armac.stable.file "td-update-mac-arm-7000007" base 7000007 must equal build 7000008`},
		{name: "leading zero rejected", build: 7000007, file: "td-update-mac-arm-07000007",
			wantErr: "base 07000007 must equal build 7000007"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadDesktopEntry(t, "armac", "stable", DesktopRelease{Build: tt.build, File: tt.file})
			assertLoadError(t, err, tt.wantErr)
		})
	}
}

func TestLoadCatalogDisabledDesktopEntrySkipsFileNameRules(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		release DesktopRelease
		wantErr string
	}{
		{name: "disabled v1 name accepted", channel: "stable",
			release: DesktopRelease{Build: 7000007, File: "tx64upd7000007", Disabled: true}},
		{name: "disabled mismatched draft accepted", channel: "beta",
			release: DesktopRelease{Build: 7000008, File: "td-update-win-x86-7000007", Disabled: true}},
		{name: "enabled v1 name rejected", channel: "stable",
			release: DesktopRelease{Build: 7000007, File: "tx64upd7000007"},
			wantErr: "must match td-update-{os}-{arch}-{base}[-beta]"},
		// 通道不属于“草稿”可以暂缓的规则：v2 根本没有 alpha，停用条目同样拒绝。
		{name: "disabled alpha still rejected", channel: "alpha",
			release: DesktopRelease{Build: 7000007, File: "td-update-win-x64-7000007", Disabled: true},
			wantErr: `desktop.win64.alpha: channel "alpha" is unsupported`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadDesktopEntry(t, "win64", tt.channel, tt.release)
			assertLoadError(t, err, tt.wantErr)
		})
	}
}

// 示例清单是运营者复制的起点：原样必须能加载，桌面条目启用后也必须满足 v2 规则。
func TestExampleManifestFollowsV2Contract(t *testing.T) {
	examplePath := filepath.Join("..", "..", "deploy", "update", "manifest.example.json")
	if _, err := LoadCatalog(examplePath, t.TempDir()); err != nil {
		t.Fatalf("example manifest: %v", err)
	}
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	var example Manifest
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	if len(example.Desktop) == 0 {
		t.Fatal("example manifest has no desktop entries")
	}
	for platform, channels := range example.Desktop {
		for channel, release := range channels {
			release.Disabled = false
			if err := loadDesktopEntry(t, platform, channel, release); err != nil {
				t.Fatalf("enabled example desktop.%s.%s: %v", platform, channel, err)
			}
		}
	}
}

func TestLoadCatalogAppsKeepAlphaChannel(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Apps: map[string]map[string]ApplicationRelease{
			"android": {"alpha": {ID: 1, Version: "12.9.1", Notes: map[string]string{"en": "New version"}}},
		},
	})
	if _, err := LoadCatalog(manifestPath, dir); err != nil {
		t.Fatalf("apps alpha channel rejected: %v", err)
	}
}
