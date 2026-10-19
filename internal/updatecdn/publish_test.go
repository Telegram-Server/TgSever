package updatecdn

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// publishEnv 是发布函数测试的目录布局：root 下的 updates/manifest.json、updates/files，
// 以及运营者上传包所在的 incoming 目录。
type publishEnv struct {
	root         string
	manifestPath string
	filesDir     string
}

func newPublishEnv(t *testing.T) publishEnv {
	t.Helper()
	root := t.TempDir()
	env := publishEnv{
		root:         root,
		manifestPath: filepath.Join(root, "updates", "manifest.json"),
		filesDir:     filepath.Join(root, "updates", "files"),
	}
	if err := os.MkdirAll(env.filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "incoming"), 0o755); err != nil {
		t.Fatal(err)
	}
	return env
}

// writePackage 把包写到 incoming 目录下的 name，返回路径。
func (e publishEnv) writePackage(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(e.root, "incoming", name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e publishEnv) publish(t *testing.T, packagePath string, opts PublishOptions) (*PublishResult, error) {
	t.Helper()
	if opts.RootPublicKey == nil {
		opts.CatalogOptions = testCatalogOptions(t)
	}
	return PublishDesktopPackage(e.manifestPath, e.filesDir, packagePath, opts)
}

// mustPublish 发布 data 并要求成功。
func (e publishEnv) mustPublish(t *testing.T, data []byte, opts PublishOptions) *PublishResult {
	t.Helper()
	result, err := e.publish(t, e.writePackage(t, "upload.bin", data), opts)
	if err != nil {
		t.Fatalf("PublishDesktopPackage() error = %v", err)
	}
	return result
}

func (e publishEnv) load(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := LoadCatalog(e.manifestPath, e.filesDir, testCatalogOptions(t))
	if err != nil {
		t.Fatalf("LoadCatalog() after publish error = %v", err)
	}
	return catalog
}

// snapshotTree 记录 root 下每个文件的内容，用来断言失败的发布没有改动任何文件。
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, root)
	for name, content := range before {
		if got, ok := after[name]; !ok {
			t.Errorf("file %s was removed", name)
		} else if got != content {
			t.Errorf("file %s was modified", name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("file %s was created", name)
		}
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func assertPublishError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("PublishDesktopPackage() error = nil, want %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("PublishDesktopPackage() error = %q, want it to contain %q", err, want)
	}
}

func TestPublishDesktopPackageDerivesEntryFromPackage(t *testing.T) {
	tests := []struct {
		platform string
		channel  string
		build    uint64
		wantFile string
	}{
		{platform: "win64", channel: "stable", build: 7000007, wantFile: "td-update-win-x64-7000007"},
		{platform: "win", channel: "beta", build: 7000008, wantFile: "td-update-win-x86-7000008-beta"},
		{platform: "armac", channel: "stable", build: 6001002, wantFile: "td-update-mac-arm-6001002"},
		{platform: "linux", channel: "beta", build: 5999999, wantFile: "td-update-linux-x64-5999999-beta"},
	}
	for _, tt := range tests {
		t.Run(tt.wantFile, func(t *testing.T) {
			env := newPublishEnv(t)
			data := testDesktopPackage(t, tt.platform, tt.channel, tt.build)
			result := env.mustPublish(t, data, PublishOptions{})

			want := DesktopRelease{Build: tt.build, File: tt.wantFile, SHA256: sha256Hex(data), Size: int64(len(data))}
			if result.Platform != tt.platform || result.Channel != tt.channel || result.Release != want {
				t.Fatalf("result = %s.%s %+v, want %s.%s %+v", result.Platform, result.Channel, result.Release, tt.platform, tt.channel, want)
			}
			if result.Unchanged {
				t.Fatal("first publish reported Unchanged")
			}
			copied, err := os.ReadFile(filepath.Join(env.filesDir, tt.wantFile))
			if err != nil || string(copied) != string(data) {
				t.Fatalf("published file = %d bytes, %v; want the package bytes", len(copied), err)
			}
			// 清单原本不存在：发布后按加载规则得到一份只含该条目的清单。
			catalog := env.load(t)
			if got := catalog.manifest.Desktop; len(got) != 1 || got[tt.platform][tt.channel] != want {
				t.Fatalf("manifest desktop = %+v", got)
			}
			if catalog.manifest.SchemaVersion != ManifestSchemaVersion {
				t.Fatalf("schema_version = %d", catalog.manifest.SchemaVersion)
			}
		})
	}
}

// seedManifest 在文件目录中放入 linux.stable 的已发布包，写出含该条目、一个停用的
// mac 草稿和移动端 apps 条目的清单，返回清单内容。
func (e publishEnv) seedManifest(t *testing.T) Manifest {
	t.Helper()
	linux := testDesktopPackage(t, "linux", "stable", 6000000)
	if err := os.WriteFile(filepath.Join(e.filesDir, "td-update-linux-x64-6000000"), linux, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"linux": {"stable": {Build: 6000000, Version: "6.0", File: "td-update-linux-x64-6000000", SHA256: sha256Hex(linux)}},
			"mac":   {"beta": {Build: 1, File: "draft", Disabled: true}},
		},
		Apps: map[string]map[string]ApplicationRelease{
			"android": {"stable": {
				ID: 77, Version: "12.9.1", URL: "https://updates.example/app.apk?a=1&b=<2>",
				URLBySource: map[string]string{"com.example.store": "https://store.example/app"},
				Notes:       map[string]string{"en": "New version"},
			}},
		},
	}
	writeTestManifest(t, filepath.Dir(e.manifestPath), manifest)
	return manifest
}

func TestPublishDesktopPackageKeepsOtherEntries(t *testing.T) {
	env := newPublishEnv(t)
	seeded := env.seedManifest(t)
	data := testDesktopPackage(t, "win64", "beta", 7000007)
	env.mustPublish(t, data, PublishOptions{})

	got := env.load(t).manifest
	if len(got.Desktop) != 3 || len(got.Desktop["win64"]) != 1 {
		t.Fatalf("desktop = %+v", got.Desktop)
	}
	if got.Desktop["linux"]["stable"] != seeded.Desktop["linux"]["stable"] || got.Desktop["mac"]["beta"] != seeded.Desktop["mac"]["beta"] {
		t.Fatalf("other desktop entries changed: %+v", got.Desktop)
	}
	app := got.Apps["android"]["stable"]
	want := seeded.Apps["android"]["stable"]
	if len(got.Apps) != 1 || app.ID != want.ID || app.URL != want.URL ||
		app.URLBySource["com.example.store"] != want.URLBySource["com.example.store"] || app.Notes["en"] != want.Notes["en"] {
		t.Fatalf("apps = %+v", got.Apps)
	}
	// 重新编码不把运营者手写的 & < > 转义成 & 之类。
	raw, err := os.ReadFile(env.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"https://updates.example/app.apk?a=1&b=<2>"`) {
		t.Fatalf("manifest rewrote the apps URL:\n%s", raw)
	}
}

func TestPublishDesktopPackageReplacesOlderBuildOnSameChannel(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	data := testDesktopPackage(t, "linux", "stable", 6000001)
	result := env.mustPublish(t, data, PublishOptions{})

	if result.Previous == nil || result.Previous.Build != 6000000 {
		t.Fatalf("Previous = %+v, want build 6000000", result.Previous)
	}
	if got := env.load(t).manifest.Desktop["linux"]["stable"]; got.Build != 6000001 || got.File != "td-update-linux-x64-6000001" {
		t.Fatalf("linux.stable = %+v", got)
	}
	// 旧包文件保留在文件目录中，已下载一半的客户端仍能续传。
	if _, err := os.Stat(filepath.Join(env.filesDir, "td-update-linux-x64-6000000")); err != nil {
		t.Fatal(err)
	}
}

func TestPublishDesktopPackageIsIdempotent(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "win64", "stable", 7000007)
	first := env.mustPublish(t, data, PublishOptions{})
	before := snapshotTree(t, env.root)

	again := env.mustPublish(t, data, PublishOptions{})
	if !again.Unchanged || again.Release != first.Release {
		t.Fatalf("republish = %+v, want Unchanged with %+v", again, first.Release)
	}
	assertTreeUnchanged(t, env.root, before)
}

func TestPublishDesktopPackageRestoresMissingFileWithoutRewritingManifest(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "win64", "stable", 7000007)
	env.mustPublish(t, data, PublishOptions{})
	published := filepath.Join(env.filesDir, "td-update-win-x64-7000007")
	if err := os.Remove(published); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(env.manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	result := env.mustPublish(t, data, PublishOptions{})
	if result.Unchanged || result.ManifestUpdated || !result.FileCopied {
		t.Fatalf("result = %+v, want only the file restored", result)
	}
	if got, err := os.ReadFile(env.manifestPath); err != nil || string(got) != string(manifestBefore) {
		t.Fatalf("manifest was rewritten: %v", err)
	}
	env.load(t)
}

func TestPublishDesktopPackageRejectsSameNameWithOtherContent(t *testing.T) {
	env := newPublishEnv(t)
	// 文件目录里已有同名（同平台、通道、build）但字节不同的包。
	other := buildTestPackage(t, func(s *testPackageSpec) { s.payload = []byte("another-payload") })
	if err := os.WriteFile(filepath.Join(env.filesDir, "td-update-win-x64-7000007"), other, 0o644); err != nil {
		t.Fatal(err)
	}
	packagePath := env.writePackage(t, "upload.bin", testDesktopPackage(t, "win64", "stable", 7000007))
	before := snapshotTree(t, env.root)

	_, err := env.publish(t, packagePath, PublishOptions{})
	assertPublishError(t, err, `published file "td-update-win-x64-7000007" already exists with different content`)
	assertTreeUnchanged(t, env.root, before)
}

func TestPublishDesktopPackageRejectsDowngradeUnlessAllowed(t *testing.T) {
	for _, build := range []uint64{5999999, 6000000} {
		env := newPublishEnv(t)
		env.seedManifest(t)
		// 现有 linux.stable 为 build 6000000；新包为 linux-x64 stable、内容不同。
		data := buildTestPackage(t, func(s *testPackageSpec) {
			s.os, s.arch, s.version = 2, 1, build<<32
			s.payload = []byte("rebuilt-payload")
		})
		if build == 6000000 {
			// 同 build 的已发布文件不在文件目录时，不同内容的包不会先因同名被拒，
			// 而是因“不大于现有 build”被拒。
			if err := os.Remove(filepath.Join(env.filesDir, "td-update-linux-x64-6000000")); err != nil {
				t.Fatal(err)
			}
		}
		packagePath := env.writePackage(t, "upload.bin", data)
		before := snapshotTree(t, env.root)

		_, err := env.publish(t, packagePath, PublishOptions{})
		assertPublishError(t, err, fmt.Sprintf("desktop.linux.stable: build %d is not newer than the published build 6000000", build))
		assertTreeUnchanged(t, env.root, before)

		if build == 6000000 {
			continue
		}
		result, err := env.publish(t, packagePath, PublishOptions{AllowDowngrade: true})
		if err != nil {
			t.Fatalf("PublishDesktopPackage(AllowDowngrade) error = %v", err)
		}
		if got := env.load(t).manifest.Desktop["linux"]["stable"]; got != result.Release || got.Build != 5999999 {
			t.Fatalf("linux.stable = %+v, want build 5999999", got)
		}
	}
}

func TestPublishDesktopPackageDoesNotReenableDisabledReleaseByDefault(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "win64", "stable", 7000007)
	first := env.mustPublish(t, data, PublishOptions{})
	// 运营者停用了这个坏版本：重跑同一个发布不能悄悄把它重新启用。
	pulled := first.Release
	pulled.Disabled = true
	writeTestManifest(t, filepath.Dir(env.manifestPath), Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop:       map[string]map[string]DesktopRelease{"win64": {"stable": pulled}},
	})
	packagePath := env.writePackage(t, "upload.bin", data)
	before := snapshotTree(t, env.root)

	_, err := env.publish(t, packagePath, PublishOptions{})
	assertPublishError(t, err, "desktop.win64.stable: build 7000007 is not newer than the published build 7000007")
	assertTreeUnchanged(t, env.root, before)

	if _, err := env.publish(t, packagePath, PublishOptions{AllowDowngrade: true}); err != nil {
		t.Fatalf("PublishDesktopPackage(AllowDowngrade) error = %v", err)
	}
	if got := env.load(t).manifest.Desktop["win64"]["stable"]; got != first.Release {
		t.Fatalf("win64.stable = %+v, want re-enabled %+v", got, first.Release)
	}
}

func TestPublishDesktopPackageRejectsWithoutChangingFiles(t *testing.T) {
	tests := []struct {
		name    string
		data    func(t *testing.T) []byte
		opts    func(t *testing.T) PublishOptions
		wantErr string
	}{
		{
			name: "bad signature",
			data: func(t *testing.T) []byte {
				return buildTestPackage(t, func(s *testPackageSpec) { s.corruptSignatures = true })
			},
			wantErr: "no valid signature",
		},
		{
			name: "signed for another root key",
			data: func(t *testing.T) []byte { return testDesktopPackage(t, "win64", "stable", 7000007) },
			opts: func(t *testing.T) PublishOptions {
				other, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				return PublishOptions{CatalogOptions: CatalogOptions{RootPublicKey: other, Now: func() time.Time { return testNow }}}
			},
			wantErr: "root",
		},
		{
			name: "no root key",
			data: func(t *testing.T) []byte { return testDesktopPackage(t, "win64", "stable", 7000007) },
			opts: func(t *testing.T) PublishOptions {
				return PublishOptions{CatalogOptions: CatalogOptions{RootPublicKey: ed25519.PublicKey{}}}
			},
			wantErr: "update root public key is required",
		},
		{
			name: "linux-arm has no platform key",
			data: func(t *testing.T) []byte {
				return buildTestPackage(t, func(s *testPackageSpec) { s.os, s.arch = 2, 2 })
			},
			wantErr: "package target linux-arm has no desktop platform key",
		},
		{
			name: "canary channel",
			data: func(t *testing.T) []byte {
				return buildTestPackage(t, func(s *testPackageSpec) {
					s.channel = 2
					s.manifestDoc["channels"].(map[string]any)["canary-public"] = []any{[]any{"ed-test"}}
				})
			},
			wantErr: "package channel canary-public cannot be published",
		},
		{
			name:    "truncated",
			data:    func(t *testing.T) []byte { return testDesktopPackage(t, "win64", "stable", 7000007)[:40] },
			wantErr: "v2 package",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newPublishEnv(t)
			env.seedManifest(t)
			packagePath := env.writePackage(t, "upload.bin", tt.data(t))
			var opts PublishOptions
			if tt.opts != nil {
				opts = tt.opts(t)
			}
			before := snapshotTree(t, env.root)

			_, err := env.publish(t, packagePath, opts)
			assertPublishError(t, err, tt.wantErr)
			assertTreeUnchanged(t, env.root, before)
		})
	}
}

func TestPublishDesktopPackageRejectsInvalidResultingManifest(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	// 现有清单里另一个条目的包已被篡改：新清单整体按加载规则校验，不能通过。
	if err := os.WriteFile(filepath.Join(env.filesDir, "td-update-linux-x64-6000000"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	packagePath := env.writePackage(t, "upload.bin", testDesktopPackage(t, "win64", "stable", 7000007))
	before := snapshotTree(t, env.root)

	for _, dryRun := range []bool{true, false} {
		_, err := env.publish(t, packagePath, PublishOptions{DryRun: dryRun})
		assertPublishError(t, err, "new manifest is invalid: desktop.linux.stable")
		// 写入模式下已拷入的新包与临时清单都被清理掉。
		assertTreeUnchanged(t, env.root, before)
	}
}

func TestPublishDesktopPackageDryRunWritesNothing(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		env := newPublishEnv(t)
		if seeded {
			env.seedManifest(t)
		}
		data := testDesktopPackage(t, "linux", "beta", 6000001)
		packagePath := env.writePackage(t, "upload.bin", data)
		before := snapshotTree(t, env.root)

		result, err := env.publish(t, packagePath, PublishOptions{DryRun: true})
		if err != nil {
			t.Fatalf("PublishDesktopPackage(DryRun) error = %v", err)
		}
		want := DesktopRelease{Build: 6000001, File: "td-update-linux-x64-6000001-beta", SHA256: sha256Hex(data), Size: int64(len(data))}
		if result.Platform != "linux" || result.Channel != "beta" || result.Release != want || result.Unchanged {
			t.Fatalf("dry-run result = %+v, want linux.beta %+v", result, want)
		}
		assertTreeUnchanged(t, env.root, before)
	}
	// 清单所在目录不存在时只校验模式也不创建它。
	root := t.TempDir()
	packagePath := filepath.Join(root, "upload.bin")
	if err := os.WriteFile(packagePath, testDesktopPackage(t, "win64", "stable", 7000007), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishDesktopPackage(filepath.Join(root, "updates", "manifest.json"), filepath.Join(root, "updates", "files"), packagePath,
		PublishOptions{CatalogOptions: testCatalogOptions(t), DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "updates")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry-run created the updates directory: %v", err)
	}
}

func TestPublishDesktopPackageCreatesMissingDirectories(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "updates", "manifest.json")
	filesDir := filepath.Join(root, "updates", "files")
	data := testDesktopPackage(t, "win64", "stable", 7000007)
	packagePath := filepath.Join(root, "upload.bin")
	if err := os.WriteFile(packagePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishDesktopPackage(manifestPath, filesDir, packagePath, PublishOptions{CatalogOptions: testCatalogOptions(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(manifestPath, filesDir, testCatalogOptions(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPublishDesktopPackageRejectsConcurrentPublish(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	if err := os.WriteFile(env.manifestPath+".lock", []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packagePath := env.writePackage(t, "upload.bin", testDesktopPackage(t, "win64", "stable", 7000007))
	before := snapshotTree(t, env.root)

	_, err := env.publish(t, packagePath, PublishOptions{})
	assertPublishError(t, err, "another publish is in progress")
	assertTreeUnchanged(t, env.root, before)
}
