package updatecdn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func (e publishEnv) setDisabled(t *testing.T, platform, channel string, disabled bool) (*SetDisabledResult, error) {
	t.Helper()
	return SetDesktopReleaseDisabled(e.manifestPath, e.filesDir, platform, channel, disabled, SetDisabledOptions{CatalogOptions: testCatalogOptions(t)})
}

func (e publishEnv) previewDisabled(t *testing.T, platform, channel string, disabled bool) (*SetDisabledResult, error) {
	t.Helper()
	return SetDesktopReleaseDisabled(e.manifestPath, e.filesDir, platform, channel, disabled, SetDisabledOptions{CatalogOptions: testCatalogOptions(t), DryRun: true})
}

func TestSetDesktopReleaseDisabledStopsServingEntry(t *testing.T) {
	env := newPublishEnv(t)
	seeded := env.seedManifest(t)

	result, err := env.setDisabled(t, "linux", "stable", true)
	if err != nil {
		t.Fatalf("SetDesktopReleaseDisabled(disable) error = %v", err)
	}
	want := seeded.Desktop["linux"]["stable"]
	want.Disabled = true
	if result.Platform != "linux" || result.Channel != "stable" || result.Unchanged || result.Release != want {
		t.Fatalf("result = %+v, want linux.stable %+v changed", result, want)
	}
	catalog := env.load(t)
	if got := catalog.manifest.Desktop["linux"]["stable"]; got != want {
		t.Fatalf("linux.stable = %+v, want %+v", got, want)
	}
	if _, ok := catalog.DesktopMap()["linux"]; ok {
		t.Fatal("disabled linux entry is still served")
	}
	// 其余条目与 apps 原样保留。
	if got := catalog.manifest.Desktop["mac"]["beta"]; got != seeded.Desktop["mac"]["beta"] {
		t.Fatalf("mac.beta = %+v, want untouched %+v", got, seeded.Desktop["mac"]["beta"])
	}
	if got := catalog.manifest.Apps["android"]["stable"].URL; got != seeded.Apps["android"]["stable"].URL {
		t.Fatalf("apps.android.stable.url = %q, want untouched", got)
	}
	if _, err := os.Stat(env.manifestPath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: %v", err)
	}
}

func TestSetDesktopReleaseDisabledReenablesWithoutDowngradeCheck(t *testing.T) {
	env := newPublishEnv(t)
	// 先发布 6000000，再回滚到 5999999 后又停用它：重新启用的是一个比曾经发布过的
	// 更旧的 build，这是显式操作，不做回退检查。
	env.mustPublish(t, testDesktopPackage(t, "linux", "stable", 6000000), PublishOptions{})
	rolledBack := env.mustPublish(t, testDesktopPackage(t, "linux", "stable", 5999999), PublishOptions{AllowDowngrade: true})
	if _, err := env.setDisabled(t, "linux", "stable", true); err != nil {
		t.Fatalf("disable error = %v", err)
	}

	result, err := env.setDisabled(t, "linux", "stable", false)
	if err != nil {
		t.Fatalf("SetDesktopReleaseDisabled(enable) error = %v", err)
	}
	if result.Unchanged || result.Release != rolledBack.Release {
		t.Fatalf("result = %+v, want re-enabled %+v", result, rolledBack.Release)
	}
	catalog := env.load(t)
	if got := catalog.manifest.Desktop["linux"]["stable"]; got != rolledBack.Release {
		t.Fatalf("linux.stable = %+v, want %+v", got, rolledBack.Release)
	}
	if got := catalog.DesktopMap()["linux"]["stable"]["released"]; got != rolledBack.Release.Build {
		t.Fatalf("served linux.stable build = %v, want %d", got, rolledBack.Release.Build)
	}
}

func TestSetDesktopReleaseDisabledIsIdempotent(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		channel  string
		disabled bool
	}{
		{name: "enable an enabled entry", platform: "linux", channel: "stable", disabled: false},
		{name: "disable a disabled entry", platform: "mac", channel: "beta", disabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newPublishEnv(t)
			seeded := env.seedManifest(t)
			info, err := os.Stat(env.manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotTree(t, env.root)

			result, err := env.setDisabled(t, tt.platform, tt.channel, tt.disabled)
			if err != nil {
				t.Fatalf("SetDesktopReleaseDisabled() error = %v", err)
			}
			if !result.Unchanged || result.Release != seeded.Desktop[tt.platform][tt.channel] {
				t.Fatalf("result = %+v, want unchanged %+v", result, seeded.Desktop[tt.platform][tt.channel])
			}
			assertTreeUnchanged(t, env.root, before)
			after, err := os.Stat(env.manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(info, after) || !after.ModTime().Equal(info.ModTime()) {
				t.Fatal("manifest was rewritten for a no-op")
			}
		})
	}
}

func TestSetDesktopReleaseDisabledRejectsWithoutChangingFiles(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(t *testing.T, env publishEnv)
		platform string
		channel  string
		disabled bool
		wantErr  string
		wantIs   error
	}{
		{
			name:     "unknown platform",
			platform: "win64", channel: "stable", disabled: true,
			wantErr: "desktop.win64.stable", wantIs: ErrDesktopEntryNotFound,
		},
		{
			name:     "unknown channel",
			platform: "linux", channel: "beta", disabled: false,
			wantErr: "desktop.linux.beta", wantIs: ErrDesktopEntryNotFound,
		},
		{
			name: "publish in progress",
			prepare: func(t *testing.T, env publishEnv) {
				if err := os.WriteFile(env.manifestPath+".lock", []byte("123\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			platform: "linux", channel: "stable", disabled: true,
			wantErr: "another publish is in progress", wantIs: ErrPublishInProgress,
		},
		{
			name: "re-enable a package that is missing",
			prepare: func(t *testing.T, env publishEnv) {
				published := env.mustPublish(t, testDesktopPackage(t, "win64", "stable", 7000007), PublishOptions{})
				if _, err := env.setDisabled(t, "win64", "stable", true); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(env.filesDir, published.Release.File)); err != nil {
					t.Fatal(err)
				}
			},
			platform: "win64", channel: "stable", disabled: false,
			wantErr: `open package "td-update-win-x64-7000007"`,
		},
		{
			name: "re-enable a package whose signature no longer verifies",
			prepare: func(t *testing.T, env publishEnv) {
				// 停用的条目只按清单字段保存：文件还在、大小与 SHA256 一致，但根签名对不上。
				data := buildTestPackage(t, func(s *testPackageSpec) { s.manifestSig = make([]byte, 64) })
				const file = "td-update-win-x64-7000007"
				if err := os.WriteFile(filepath.Join(env.filesDir, file), data, 0o644); err != nil {
					t.Fatal(err)
				}
				manifest := env.seedManifest(t)
				manifest.Desktop["win64"] = map[string]DesktopRelease{"stable": {
					Build: 7000007, File: file, SHA256: sha256Hex(data), Size: int64(len(data)), Disabled: true,
				}}
				writeTestManifest(t, filepath.Dir(env.manifestPath), manifest)
			},
			platform: "win64", channel: "stable", disabled: false,
			wantErr: "bad manifest root signature",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newPublishEnv(t)
			env.seedManifest(t)
			if tt.prepare != nil {
				tt.prepare(t, env)
			}
			before := snapshotTree(t, env.root)

			_, err := env.setDisabled(t, tt.platform, tt.channel, tt.disabled)
			assertPublishError(t, err, tt.wantErr)
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Fatalf("error = %v, want errors.Is %v", err, tt.wantIs)
			}
			assertTreeUnchanged(t, env.root, before)
		})
	}
}

func TestStoreSetDesktopReleaseDisabledIsPickedUpBySnapshot(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	store, err := NewStore(env.manifestPath, env.filesDir, testCatalogOptions(t))
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.SetDesktopReleaseDisabled("linux", "stable", true, false)
	if err != nil || !result.Release.Disabled {
		t.Fatalf("Store.SetDesktopReleaseDisabled() = %+v, %v", result, err)
	}
	// 不需要通知 Store：下一次快照按清单的 stat 与内容发现变化。
	catalog, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.DesktopMap()["linux"]; ok {
		t.Fatal("store still serves the disabled linux entry")
	}
}

func TestSetDesktopReleaseDisabledDryRunValidatesWithoutWriting(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	published := env.mustPublish(t, testDesktopPackage(t, "win64", "stable", 7000007), PublishOptions{})
	if _, err := env.setDisabled(t, "win64", "stable", true); err != nil {
		t.Fatal(err)
	}
	// 只校验时不取锁，与发布的 dry-run 一致：预览不会和正在进行的发布冲突。
	if err := os.WriteFile(env.manifestPath+".lock", []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, env.root)

	result, err := env.previewDisabled(t, "win64", "stable", false)
	if err != nil {
		t.Fatalf("dry-run enable error = %v", err)
	}
	if result.Unchanged || result.Release != published.Release {
		t.Fatalf("dry-run enable = %+v, want would-enable %+v", result, published.Release)
	}
	assertTreeUnchanged(t, env.root, before)

	if err := os.Remove(filepath.Join(env.filesDir, published.Release.File)); err != nil {
		t.Fatal(err)
	}
	before = snapshotTree(t, env.root)
	_, err = env.previewDisabled(t, "win64", "stable", false)
	assertPublishError(t, err, `open package "td-update-win-x64-7000007"`)
	assertTreeUnchanged(t, env.root, before)
}
