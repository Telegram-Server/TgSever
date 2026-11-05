package updatecdn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"telesrv/internal/apkinfo"
)

// Android 安装包的发布、加载校验、身份核对、停用与启用、文件来源。样本是 internal/apkinfo
// 的真实签名 APK（见那里的 testdata/README.md）：同一个密钥 A 签的 1234 与 1300 两个版本、
// 密钥 B 签的 1234，以及未签名的包。

const (
	apkV1234    = "a-v1234-v2v3.apk"
	apkV1300    = "a-v1300-v2v3.apk"
	apkKeyB     = "b-v1234-v2v3.apk"
	apkUnsigned = "unsigned-v1234.apk"
	apkPackage  = "cc.example.app"
	apkCertA    = "036c7da6ef8e90cb5731f0d9270ff204b33347b840425e24e9b7d34a30076b2e"
)

func apkFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "apkinfo", "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return path
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type androidCatalog struct {
	manifest string
	files    string
}

func newAndroidCatalog(t *testing.T) androidCatalog {
	t.Helper()
	dir := t.TempDir()
	return androidCatalog{manifest: filepath.Join(dir, "manifest.json"), files: filepath.Join(dir, "files")}
}

func stableMeta() AndroidReleaseMeta {
	return AndroidReleaseMeta{Channel: "stable", Notes: map[string]string{"en": "Bug fixes", "ZH_Hans": "修复若干问题"}}
}

func (c androidCatalog) publish(t *testing.T, apk string, meta AndroidReleaseMeta, opts PublishOptions) *AndroidPublishResult {
	t.Helper()
	result, err := PublishAndroidPackage(c.manifest, c.files, apkFixture(t, apk), meta, opts)
	if err != nil {
		t.Fatalf("publish %s: %v", apk, err)
	}
	return result
}

func (c androidCatalog) load(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := LoadCatalog(c.manifest, c.files, CatalogOptions{})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return catalog
}

func TestPublishAndroidPackageWritesEntryAndResolvesWithFile(t *testing.T) {
	c := newAndroidCatalog(t)
	result := c.publish(t, apkV1234, stableMeta(), PublishOptions{})
	if !result.ManifestUpdated || !result.FileCopied || result.Unchanged || result.Previous != nil || result.Downgrade {
		t.Fatalf("result = %+v", result)
	}
	release := result.Release
	wantSHA := fileSHA256(t, apkFixture(t, apkV1234))
	if release.ID != 1234 || release.Version != "12.2.10" || release.VersionCode != 1234 || release.PackageName != apkPackage ||
		release.CertSHA256 != apkCertA || release.SHA256 != wantSHA || release.File != "android-cc.example.app-1234.apk" {
		t.Fatalf("release = %+v", release)
	}
	// 语言键统一成小写、- 分隔。
	if release.Notes["zh-hans"] != "修复若干问题" || release.Notes["en"] != "Bug fixes" {
		t.Fatalf("notes = %v", release.Notes)
	}
	if got := fileSHA256(t, filepath.Join(c.files, release.File)); got != wantSHA {
		t.Fatalf("copied file sha = %s, want %s", got, wantSHA)
	}

	catalog := c.load(t)
	resolved, err := catalog.Resolve(ResolveRequest{Platform: "android", Version: "12.2.9 (1200)", LangCode: "zh-hans"})
	if err != nil || resolved == nil {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	if resolved.File == nil || resolved.File.Name != release.File || resolved.File.SHA256 != wantSHA ||
		resolved.File.Size != release.Size || resolved.File.MimeType != AndroidPackageMimeType || resolved.Text != "修复若干问题" {
		t.Fatalf("resolved = %+v file=%+v", resolved, resolved.File)
	}
	if same, _ := catalog.Resolve(ResolveRequest{Platform: "android", Version: "12.2.10 (1234)"}); same != nil {
		t.Fatalf("same version resolved to %+v", same)
	}
	if files := catalog.AppFiles(); len(files) != 1 || files[0] != *resolved.File {
		t.Fatalf("AppFiles = %+v", files)
	}
}

func TestPublishAndroidPackageRepublishAndMetadataEdit(t *testing.T) {
	c := newAndroidCatalog(t)
	c.publish(t, apkV1234, stableMeta(), PublishOptions{})
	again := c.publish(t, apkV1234, stableMeta(), PublishOptions{})
	if !again.Unchanged || again.ManifestUpdated || again.FileCopied {
		t.Fatalf("republish = %+v, want unchanged", again)
	}
	// 同一个包只改说明与强制：更新清单，不算回退，不重新拷文件。
	meta := stableMeta()
	meta.Notes = map[string]string{"en": "Important fixes"}
	meta.CanNotSkip = true
	edit := c.publish(t, apkV1234, meta, PublishOptions{})
	if !edit.ManifestUpdated || edit.FileCopied || edit.Downgrade || !edit.Release.CanNotSkip {
		t.Fatalf("metadata edit = %+v", edit)
	}
	resolved, _ := c.load(t).Resolve(ResolveRequest{Platform: "android", Version: "1.0", LangCode: "ru"})
	if resolved == nil || !resolved.CanNotSkip || resolved.Text != "Important fixes" {
		t.Fatalf("resolved after edit = %+v", resolved)
	}
}

func TestPublishAndroidPackageVersionOrder(t *testing.T) {
	c := newAndroidCatalog(t)
	c.publish(t, apkV1234, stableMeta(), PublishOptions{})
	newer := c.publish(t, apkV1300, stableMeta(), PublishOptions{})
	if newer.Previous == nil || newer.Previous.VersionCode != 1234 || newer.Downgrade {
		t.Fatalf("upgrade = %+v", newer)
	}
	if _, err := PublishAndroidPackage(c.manifest, c.files, apkFixture(t, apkV1234), stableMeta(), PublishOptions{}); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("downgrade err = %v, want not newer", err)
	}
	rollback := c.publish(t, apkV1234, stableMeta(), PublishOptions{AllowDowngrade: true})
	if !rollback.Downgrade || rollback.Release.VersionCode != 1234 {
		t.Fatalf("rollback = %+v", rollback)
	}
	// 各通道独立：beta 上可以发布更新的版本。
	beta := stableMeta()
	beta.Channel = "beta"
	if r := c.publish(t, apkV1300, beta, PublishOptions{}); r.Previous != nil || r.FileCopied {
		t.Fatalf("beta publish of an already-copied file = %+v", r)
	}
	if files := c.load(t).AppFiles(); len(files) != 2 {
		t.Fatalf("AppFiles = %+v, want both packages", files)
	}
}

func TestPublishAndroidPackageRejectsAnotherSigner(t *testing.T) {
	c := newAndroidCatalog(t)
	c.publish(t, apkV1234, stableMeta(), PublishOptions{})
	beta := stableMeta()
	beta.Channel = "beta"
	for _, meta := range []AndroidReleaseMeta{stableMeta(), beta} {
		_, err := PublishAndroidPackage(c.manifest, c.files, apkFixture(t, apkKeyB), meta, PublishOptions{AllowDowngrade: true})
		if !errors.Is(err, ErrAndroidIdentityMismatch) {
			t.Fatalf("%s: publish with another signer err = %v, want ErrAndroidIdentityMismatch", meta.Channel, err)
		}
	}
	if _, err := InspectAndroidPackage(c.manifest, apkFixture(t, apkKeyB)); !errors.Is(err, ErrAndroidIdentityMismatch) {
		t.Fatalf("inspect err = %v, want ErrAndroidIdentityMismatch", err)
	}
	// 没有发布过时任何签名都可以。
	fresh := newAndroidCatalog(t)
	if inspected, err := InspectAndroidPackage(fresh.manifest, apkFixture(t, apkKeyB)); err != nil || inspected.APK.PackageName != apkPackage || len(inspected.Current) != 0 {
		t.Fatalf("inspect fresh = %+v, %v", inspected, err)
	}
}

func TestPublishAndroidPackageRejectsInvalidInput(t *testing.T) {
	c := newAndroidCatalog(t)
	cases := map[string]AndroidReleaseMeta{
		"channel":    {Channel: "nightly", Notes: map[string]string{"en": "x"}},
		"no notes":   {Channel: "stable", Notes: map[string]string{"en": "  "}},
		"bad url":    {Channel: "stable", Notes: map[string]string{"en": "x"}, URL: "ftp://example.com/a.apk"},
		"long notes": {Channel: "stable", Notes: map[string]string{"en": strings.Repeat("x", 5000)}},
	}
	for name, meta := range cases {
		if _, err := PublishAndroidPackage(c.manifest, c.files, apkFixture(t, apkV1234), meta, PublishOptions{}); !errors.Is(err, ErrAndroidReleaseInvalid) {
			t.Fatalf("%s: err = %v, want ErrAndroidReleaseInvalid", name, err)
		}
	}
	if _, err := PublishAndroidPackage(c.manifest, c.files, apkFixture(t, apkUnsigned), stableMeta(), PublishOptions{}); !errors.Is(err, apkinfo.ErrUnsigned) {
		t.Fatalf("unsigned err = %v, want apkinfo.ErrUnsigned", err)
	}
	if _, err := os.Stat(c.manifest); !os.IsNotExist(err) {
		t.Fatalf("rejected publishes wrote a manifest: %v", err)
	}
}

func TestPublishAndroidPackageDryRunWritesNothing(t *testing.T) {
	c := newAndroidCatalog(t)
	result := c.publish(t, apkV1234, stableMeta(), PublishOptions{DryRun: true})
	if !result.ManifestUpdated || !result.FileCopied {
		t.Fatalf("dry run = %+v", result)
	}
	if _, err := os.Stat(c.manifest); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.files, result.Release.File)); !os.IsNotExist(err) {
		t.Fatalf("dry run copied the package: %v", err)
	}
}

func TestCatalogVerifiesAndroidFiles(t *testing.T) {
	c := newAndroidCatalog(t)
	release := c.publish(t, apkV1234, stableMeta(), PublishOptions{}).Release
	manifest, _, err := readManifestFile(c.manifest)
	if err != nil {
		t.Fatal(err)
	}
	write := func(t *testing.T, m Manifest) {
		t.Helper()
		data, err := encodeManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.manifest, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(mutate func(*ApplicationRelease)) Manifest {
		data, _ := encodeManifest(manifest)
		m, _ := decodeManifest(strings.NewReader(string(data)))
		entry := m.Apps["android"]["stable"]
		mutate(&entry)
		m.Apps["android"]["stable"] = entry
		return m
	}
	bad := map[string]Manifest{
		"version code": edit(func(r *ApplicationRelease) { r.VersionCode = 9999 }),
		"package":      edit(func(r *ApplicationRelease) { r.PackageName = "cc.other.app" }),
		"signer":       edit(func(r *ApplicationRelease) { r.CertSHA256 = strings.Repeat("ab", 32) }),
		"version name": edit(func(r *ApplicationRelease) { r.Version = "12.2.11" }),
		"sha256":       edit(func(r *ApplicationRelease) { r.SHA256 = strings.Repeat("0", 64) }),
		"missing file": edit(func(r *ApplicationRelease) { r.File = "android-missing-1.apk" }),
		"bad name":     edit(func(r *ApplicationRelease) { r.File = "../escape.apk" }),
		"no metadata":  edit(func(r *ApplicationRelease) { r.CertSHA256 = "" }),
	}
	iosEntry := edit(func(*ApplicationRelease) {})
	iosEntry.Apps["ios"] = map[string]ApplicationRelease{"stable": release}
	bad["file on ios"] = iosEntry
	for name, m := range bad {
		write(t, m)
		if _, err := LoadCatalog(c.manifest, c.files, CatalogOptions{}); err == nil {
			t.Fatalf("%s: catalog loaded, want a validation error", name)
		}
	}
	// 停用的条目不校验文件。
	disabled := edit(func(r *ApplicationRelease) { r.File = "android-missing-1.apk"; r.Disabled = true })
	write(t, disabled)
	if _, err := LoadCatalog(c.manifest, c.files, CatalogOptions{}); err != nil {
		t.Fatalf("disabled entry with a missing file: %v", err)
	}
	// 文件被换成另一个合法 APK：哈希不符。
	write(t, manifest)
	tampered, _ := os.ReadFile(apkFixture(t, apkV1300))
	if err := os.WriteFile(filepath.Join(c.files, release.File), tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(c.manifest, c.files, CatalogOptions{}); err == nil {
		t.Fatal("catalog loaded a swapped package file")
	}
}

func TestStagedAndroidPackagePublishAndToggle(t *testing.T) {
	c := newAndroidCatalog(t)
	c.publish(t, apkV1234, stableMeta(), PublishOptions{})
	store, err := NewStore(c.manifest, c.files, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(apkFixture(t, apkV1300))
	if err != nil {
		t.Fatal(err)
	}
	staged, err := store.StagePackage(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if kind, err := store.StagedPackageKind(staged.ID); err != nil || kind != PackageKindAndroid {
		t.Fatalf("kind = %q, %v", kind, err)
	}
	inspected, err := store.InspectStagedAndroidPackage(staged.ID)
	if err != nil || inspected.APK.VersionCode != 1300 || inspected.Current["stable"].VersionCode != 1234 || inspected.SHA256 != staged.SHA256 {
		t.Fatalf("inspect = %+v, %v", inspected, err)
	}
	if _, err := store.PublishStagedAndroidPackage(staged.ID, stableMeta(), true, false); err != nil {
		t.Fatalf("staged dry run: %v", err)
	}
	result, err := store.PublishStagedAndroidPackage(staged.ID, stableMeta(), false, false)
	if err != nil || result.Release.VersionCode != 1300 {
		t.Fatalf("staged publish = %+v, %v", result, err)
	}
	if _, err := store.InspectStagedAndroidPackage(staged.ID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("staging after publish err = %v, want removed", err)
	}

	// 停用：不再解析出更新、不再列出安装包；启用后恢复。
	if r, err := store.SetAppReleaseDisabled("android", "stable", true, false); err != nil || !r.Release.Disabled {
		t.Fatalf("disable = %+v, %v", r, err)
	}
	if resolved, _ := store.Resolve(context.Background(), ResolveRequest{Platform: "android", Version: "1.0"}); resolved != nil {
		t.Fatalf("disabled entry resolved to %+v", resolved)
	}
	if files, _ := store.AppFiles(context.Background()); len(files) != 0 {
		t.Fatalf("disabled entry still lists %+v", files)
	}
	if r, err := store.SetAppReleaseDisabled("android", "stable", true, false); err != nil || !r.Unchanged {
		t.Fatalf("disable twice = %+v, %v", r, err)
	}
	if _, err := store.SetAppReleaseDisabled("android", "beta", true, false); !errors.Is(err, ErrDesktopEntryNotFound) {
		t.Fatalf("missing entry err = %v", err)
	}
	// 启用前安装包被删了：拒绝，清单不变。
	path := filepath.Join(c.files, result.Release.File)
	data, _ := os.ReadFile(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAppReleaseDisabled("android", "stable", false, false); err == nil {
		t.Fatal("re-enabled an entry whose package is gone")
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := store.SetAppReleaseDisabled("android", "stable", false, false); err != nil || r.Release.Disabled {
		t.Fatalf("enable = %+v, %v", r, err)
	}
	if resolved, _ := store.Resolve(context.Background(), ResolveRequest{Platform: "android", Version: "1.0"}); resolved == nil || resolved.File == nil {
		t.Fatalf("re-enabled entry resolved to %+v", resolved)
	}
}

func TestAndroidFilesAreServedAndVerified(t *testing.T) {
	c := newAndroidCatalog(t)
	release := c.publish(t, apkV1234, stableMeta(), PublishOptions{}).Release
	store, err := NewStore(c.manifest, c.files, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want, _ := os.ReadFile(apkFixture(t, apkV1234))

	for name, source := range map[string]AppFileSource{"store": store, "client": client} {
		files, err := source.AppFiles(ctx)
		if err != nil || len(files) != 1 || files[0].Name != release.File {
			t.Fatalf("%s: AppFiles = %+v, %v", name, files, err)
		}
		body, err := source.OpenAppFile(ctx, files[0])
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		got, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s: read %d bytes, err %v", name, len(got), err)
		}
		// 记录的 SHA256 与文件不符：读到末尾报错。
		wrong := files[0]
		wrong.SHA256 = strings.Repeat("0", 64)
		if name == "store" {
			if _, err := source.OpenAppFile(ctx, wrong); !errors.Is(err, ErrAppFileNotFound) {
				t.Fatalf("store: open with a wrong sha err = %v", err)
			}
		} else {
			body, err := source.OpenAppFile(ctx, wrong)
			if err != nil {
				t.Fatalf("client: open: %v", err)
			}
			_, err = io.ReadAll(body)
			_ = body.Close()
			if !errors.Is(err, ErrAppFileMismatch) {
				t.Fatalf("client: read with a wrong sha err = %v, want ErrAppFileMismatch", err)
			}
		}
	}

	// /v1/resolve 带上 file，独立模式的 Client 能解出它。
	resolved, err := client.Resolve(ctx, ResolveRequest{Platform: "android", Version: "1.0", LangCode: "en"})
	if err != nil || resolved == nil || resolved.File == nil || resolved.File.Name != release.File {
		t.Fatalf("client resolve = %+v, %v", resolved, err)
	}
	resp, err := http.Get(server.URL + "/v1/app-files")
	if err != nil {
		t.Fatal(err)
	}
	var listed appFilesResponse
	err = json.NewDecoder(resp.Body).Decode(&listed)
	_ = resp.Body.Close()
	if err != nil || len(listed.Files) != 1 {
		t.Fatalf("/v1/app-files = %+v, %v", listed, err)
	}
}

func TestSniffPackageKind(t *testing.T) {
	if kind, err := SniffPackageKind(apkFixture(t, apkV1234)); err != nil || kind != PackageKindAndroid {
		t.Fatalf("apk kind = %q, %v", kind, err)
	}
	other := filepath.Join(t.TempDir(), "pkg")
	if err := os.WriteFile(other, []byte("TD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if kind, err := SniffPackageKind(other); err != nil || kind != PackageKindDesktop {
		t.Fatalf("short file kind = %q, %v", kind, err)
	}
}
