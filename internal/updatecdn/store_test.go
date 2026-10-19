package updatecdn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appsManifest 返回只含一个 iOS 移动端条目的清单；不同 id 序列化后长度相同，
// 便于构造“同大小改写”。
func appsManifest(id int) Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Apps: map[string]map[string]ApplicationRelease{
			"ios": {"stable": {ID: id, Version: "12.9.1", URL: "https://apps.apple.com/app/id1", Notes: map[string]string{"en": "Update available"}}},
		},
	}
}

// setManifestTime 把清单的 mtime 固定为 at，模拟时间戳精度粗的文件系统。
func setManifestTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func resolveIOSUpdateID(t *testing.T, resolver Resolver) int {
	t.Helper()
	resolved, err := resolver.Resolve(t.Context(), ResolveRequest{Platform: "ios", Version: "12.9.0", LangCode: "en"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved == nil {
		t.Fatal("Resolve() = nil, want an update")
	}
	return resolved.ID
}

func TestStoreResolvesApplicationUpdatesInProcess(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, dir, appsManifest(8))
	store, err := NewStore(manifestPath, filepath.Join(dir, "files"), CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var resolver Resolver = store
	if id := resolveIOSUpdateID(t, resolver); id != 8 {
		t.Fatalf("resolved id = %d, want 8", id)
	}
	resolved, err := resolver.Resolve(t.Context(), ResolveRequest{Platform: "ios", Version: "12.9.1"})
	if err != nil || resolved != nil {
		t.Fatalf("Resolve() for the current version = %#v, %v; want no update", resolved, err)
	}
}

func TestStoreKeepsLastGoodSnapshotWhenReloadFails(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, dir, appsManifest(8))
	var reloadErrors []error
	store, err := NewStore(manifestPath, filepath.Join(dir, "files"), CatalogOptions{},
		WithReloadErrorHandler(func(err error) { reloadErrors = append(reloadErrors, err) }))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(manifestPath, []byte(`{"schema_version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	setManifestTime(t, manifestPath, time.Now().Add(-time.Hour))
	for range 3 {
		catalog, err := store.Snapshot()
		if err != nil || catalog == nil {
			t.Fatalf("Snapshot() after a broken reload = %v, %v; want the previous catalog", catalog, err)
		}
		if id := resolveIOSUpdateID(t, store); id != 8 {
			t.Fatalf("resolved id = %d, want the previous snapshot's 8", id)
		}
	}
	if len(reloadErrors) != 1 {
		t.Fatalf("reload attempts reported = %d (%v), want the broken manifest loaded exactly once", len(reloadErrors), reloadErrors)
	}
	if store.Err() == nil {
		t.Fatal("Err() = nil while serving a stale snapshot")
	}

	writeTestManifest(t, dir, appsManifest(9))
	setManifestTime(t, manifestPath, time.Now().Add(-30*time.Minute))
	if id := resolveIOSUpdateID(t, store); id != 9 {
		t.Fatalf("resolved id after the manifest was fixed = %d, want 9", id)
	}
	if err := store.Err(); err != nil {
		t.Fatalf("Err() after a successful reload = %v", err)
	}
}

func TestStoreKeepsLastGoodSnapshotWhenManifestDisappears(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, dir, appsManifest(8))
	var reloadErrors []error
	store, err := NewStore(manifestPath, filepath.Join(dir, "files"), CatalogOptions{},
		WithReloadErrorHandler(func(err error) { reloadErrors = append(reloadErrors, err) }))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if id := resolveIOSUpdateID(t, store); id != 8 {
			t.Fatalf("resolved id = %d, want the previous snapshot's 8", id)
		}
	}
	if len(reloadErrors) != 1 || store.Err() == nil {
		t.Fatalf("reload errors = %v, Err() = %v; want one reported error", reloadErrors, store.Err())
	}
}

func TestStoreDetectsSameSizeRewriteWithinTimestampGranularity(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTestManifest(t, dir, appsManifest(8))
	tick := time.Now().Truncate(time.Second)
	setManifestTime(t, manifestPath, tick)
	store, err := NewStore(manifestPath, filepath.Join(dir, "files"), CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	writeTestManifest(t, dir, appsManifest(9))
	setManifestTime(t, manifestPath, tick)
	after, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("test setup: rewrite changed size or mtime (%d %v -> %d %v)", before.Size(), before.ModTime(), after.Size(), after.ModTime())
	}
	if id := resolveIOSUpdateID(t, store); id != 9 {
		t.Fatalf("resolved id after a same-size rewrite = %d, want 9", id)
	}
}

func TestStoreFirstLoadFailureIsAnError(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(manifestPath, filepath.Join(dir, "files"), CatalogOptions{}); err == nil {
		t.Fatal("NewStore() accepted a broken manifest")
	}
	if _, err := NewStore(filepath.Join(dir, "missing.json"), filepath.Join(dir, "files"), CatalogOptions{}); err == nil ||
		!strings.Contains(err.Error(), "manifest") {
		t.Fatalf("NewStore() for a missing manifest error = %v", err)
	}
}
