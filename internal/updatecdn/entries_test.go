package updatecdn

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 管理后台的条目列表要包含停用条目；启用条目的大小与打包时间取自校验过的包。
func TestCatalogDesktopReleasesListsEveryEntryIncludingDisabled(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	beta := testDesktopPackage(t, "win64", "beta", 6100000)
	env.mustPublish(t, beta, PublishOptions{})

	entries := env.load(t).DesktopReleases()
	if len(entries) != 3 {
		t.Fatalf("DesktopReleases() = %d entries (%+v), want 3", len(entries), entries)
	}
	// 按平台、通道排序。
	wantKeys := []string{"linux.stable", "mac.beta", "win64.beta"}
	for i, entry := range entries {
		if got := entry.Platform + "." + entry.Channel; got != wantKeys[i] {
			t.Fatalf("entry %d = %s, want %s", i, got, wantKeys[i])
		}
	}

	linux := entries[0]
	linuxData, err := os.ReadFile(env.filesDir + "/td-update-linux-x64-6000000")
	if err != nil {
		t.Fatal(err)
	}
	if linux.Release.Build != 6000000 || linux.Release.File != "td-update-linux-x64-6000000" ||
		linux.Release.SHA256 != sha256Hex(linuxData) || linux.Release.Disabled {
		t.Fatalf("linux.stable = %+v", linux.Release)
	}
	// 手写清单没有 size 字段时，大小来自已校验的包文件。
	if linux.Release.Size != int64(len(linuxData)) {
		t.Fatalf("linux.stable size = %d, want %d", linux.Release.Size, len(linuxData))
	}
	if want := time.Unix(1760000000, 0); !linux.Created.Equal(want) {
		t.Fatalf("linux.stable created = %v, want %v", linux.Created, want)
	}

	draft := entries[1]
	if !draft.Release.Disabled || draft.Release.Build != 1 || draft.Release.File != "draft" {
		t.Fatalf("mac.beta = %+v, want the disabled draft", draft.Release)
	}
	if !draft.Created.IsZero() {
		t.Fatalf("disabled entry created = %v, want zero (its package is not verified)", draft.Created)
	}

	if got := entries[2].Release; got.Build != 6100000 || got.SHA256 != sha256Hex(beta) || got.Size != int64(len(beta)) {
		t.Fatalf("win64.beta = %+v", got)
	}
}

// Status 在同一把锁下给出当前快照与重新加载失败的原因：清单改坏后仍报告上一份
// 正常快照的条目，并给出原因；修好后原因清空。
func TestStoreStatusReportsEntriesAndReloadError(t *testing.T) {
	env := newPublishEnv(t)
	env.seedManifest(t)
	store, err := NewStore(env.manifestPath, env.filesDir, testCatalogOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if store.ManifestPath() != env.manifestPath || store.FilesDir() != env.filesDir {
		t.Fatalf("ManifestPath() = %q, FilesDir() = %q", store.ManifestPath(), store.FilesDir())
	}
	if store.CatalogOptions().RootPublicKey == nil {
		t.Fatal("CatalogOptions() lost the root public key")
	}

	status, err := store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.ReloadErr != nil || len(status.Desktop) != 2 {
		t.Fatalf("Status() = %+v, want two entries and no reload error", status)
	}

	good, err := os.ReadFile(env.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.manifestPath, []byte(`{"schema_version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	setManifestTime(t, env.manifestPath, time.Now().Add(-time.Hour))
	status, err = store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.ReloadErr == nil || !strings.Contains(status.ReloadErr.Error(), "manifest") {
		t.Fatalf("Status().ReloadErr = %v, want the manifest decode error", status.ReloadErr)
	}
	if len(status.Desktop) != 2 || status.Desktop[0].Platform != "linux" {
		t.Fatal("Status() after a broken reload lost the previous snapshot's entries")
	}

	if err := os.WriteFile(env.manifestPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	setManifestTime(t, env.manifestPath, time.Now().Add(-30*time.Minute))
	if status, err = store.Status(); err != nil || status.ReloadErr != nil {
		t.Fatalf("Status() after the manifest was fixed = %v, %v", status.ReloadErr, err)
	}
}
