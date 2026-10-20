package updatecdn

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stagingDir 是 env 的暂存目录：清单所在目录下的 .staging。
func (e publishEnv) stagingDir() string {
	return filepath.Join(filepath.Dir(e.manifestPath), ".staging")
}

func (e publishEnv) stage(t *testing.T, data []byte, now time.Time) StagedPackage {
	t.Helper()
	staged, err := StagePackage(e.manifestPath, bytes.NewReader(data), now, DefaultStagingTTL)
	if err != nil {
		t.Fatalf("StagePackage() error = %v", err)
	}
	return staged
}

func stagedOptions(t *testing.T, dryRun, allowDowngrade bool) PublishOptions {
	t.Helper()
	return PublishOptions{CatalogOptions: testCatalogOptions(t), DryRun: dryRun, AllowDowngrade: allowDowngrade}
}

// setStagingLimit 把暂存上限临时调小，测试不必构造 256 MiB 的数据。
func setStagingLimit(t *testing.T, limit int64) {
	t.Helper()
	previous := stagingSizeLimit
	stagingSizeLimit = limit
	t.Cleanup(func() { stagingSizeLimit = previous })
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestStagePackageWritesUnguessableFileUnderDotDirectory(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	first := env.stage(t, data, testNow)
	second := env.stage(t, data, testNow)

	if len(first.ID) != 32 || strings.Trim(first.ID, "0123456789abcdef") != "" {
		t.Fatalf("staging id = %q, want 32 lowercase hex characters", first.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("two stagings share the id %q", first.ID)
	}
	if first.Size != int64(len(data)) || first.SHA256 != sha256Hex(data) {
		t.Fatalf("staged = %+v, want size %d and sha256 %s", first, len(data), sha256Hex(data))
	}
	got, err := os.ReadFile(filepath.Join(env.stagingDir(), first.ID))
	if err != nil {
		t.Fatalf("staged file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("staged file content differs from the upload")
	}
	path, err := StagedPackagePath(env.manifestPath, first.ID, testNow, DefaultStagingTTL)
	if err != nil || path != filepath.Join(env.stagingDir(), first.ID) {
		t.Fatalf("StagedPackagePath() = %q, %v", path, err)
	}
}

func TestStagePackageAbortsOverLimitWithoutLeavingFiles(t *testing.T) {
	env := newPublishEnv(t)
	setStagingLimit(t, 1024)
	// 读取方在超过上限后最多再多读一个字节：上限之外的数据从未被消费完。
	reader := &countingReader{r: bytes.NewReader(make([]byte, 64*1024))}
	_, err := StagePackage(env.manifestPath, reader, testNow, DefaultStagingTTL)
	if !errors.Is(err, ErrStagedPackageTooLarge) {
		t.Fatalf("StagePackage() error = %v, want ErrStagedPackageTooLarge", err)
	}
	if reader.n > 1024+32*1024 {
		t.Fatalf("read %d bytes of an over-limit upload, want the transfer cut near the limit", reader.n)
	}
	if names := dirNames(t, env.stagingDir()); len(names) != 0 {
		t.Fatalf("staging directory holds %v after an over-limit upload", names)
	}
}

func TestStagePackageAcceptsExactlyTheLimit(t *testing.T) {
	env := newPublishEnv(t)
	setStagingLimit(t, 1024)
	staged, err := StagePackage(env.manifestPath, bytes.NewReader(make([]byte, 1024)), testNow, DefaultStagingTTL)
	if err != nil || staged.Size != 1024 {
		t.Fatalf("StagePackage() = %+v, %v; want 1024 bytes staged", staged, err)
	}
}

func TestStagePackageReadErrorLeavesNoFiles(t *testing.T) {
	env := newPublishEnv(t)
	broken := io.MultiReader(bytes.NewReader(make([]byte, 4096)), errReader{errors.New("connection reset")})
	_, err := StagePackage(env.manifestPath, broken, testNow, DefaultStagingTTL)
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("StagePackage() error = %v, want the read error", err)
	}
	if names := dirNames(t, env.stagingDir()); len(names) != 0 {
		t.Fatalf("staging directory holds %v after a failed read", names)
	}
}

func TestCleanupStagedPackagesRemovesOnlyExpired(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	old := env.stage(t, data, testNow.Add(-31*time.Minute))
	fresh := env.stage(t, data, testNow.Add(-29*time.Minute))
	// 中断的上传留下的 .part 文件同样按时间清理。
	part := filepath.Join(env.stagingDir(), "0123456789abcdef0123456789abcdef.part")
	if err := os.WriteFile(part, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(part, testNow.Add(-time.Hour), testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanupStagedPackages(env.manifestPath, testNow, DefaultStagingTTL)
	if err != nil {
		t.Fatalf("CleanupStagedPackages() error = %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want the expired package and the stale part file", removed)
	}
	if names := dirNames(t, env.stagingDir()); !reflect.DeepEqual(names, []string{fresh.ID}) {
		t.Fatalf("staging directory = %v, want only %s", names, fresh.ID)
	}
	if _, err := StagedPackagePath(env.manifestPath, old.ID, testNow, DefaultStagingTTL); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("StagedPackagePath(expired, cleaned) error = %v, want ErrStagingNotFound", err)
	}
}

func TestStagePackageCleansExpiredStagings(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	old := env.stage(t, data, testNow.Add(-time.Hour))
	env.stage(t, data, testNow)
	if _, err := os.Stat(filepath.Join(env.stagingDir(), old.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired staging survived a new upload: %v", err)
	}
}

func TestStagedPackagePathReportsExpiredAndRemovesIt(t *testing.T) {
	env := newPublishEnv(t)
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	_, err := StagedPackagePath(env.manifestPath, staged.ID, testNow.Add(DefaultStagingTTL+time.Second), DefaultStagingTTL)
	if !errors.Is(err, ErrStagingExpired) {
		t.Fatalf("StagedPackagePath() error = %v, want ErrStagingExpired", err)
	}
	if names := dirNames(t, env.stagingDir()); len(names) != 0 {
		t.Fatalf("expired staging left behind: %v", names)
	}
}

func TestStagedPackagePathRejectsMalformedIDs(t *testing.T) {
	env := newPublishEnv(t)
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	// 清单本身、上级目录与 .part 文件都不能通过暂存 id 访问。
	for _, id := range []string{"", "../manifest.json", "..", staged.ID + ".part", strings.ToUpper(staged.ID), staged.ID + "/", "/" + staged.ID, staged.ID[:31]} {
		if _, err := StagedPackagePath(env.manifestPath, id, testNow, DefaultStagingTTL); !errors.Is(err, ErrStagingNotFound) {
			t.Errorf("StagedPackagePath(%q) error = %v, want ErrStagingNotFound", id, err)
		}
		if err := DiscardStagedPackage(env.manifestPath, id); !errors.Is(err, ErrStagingNotFound) {
			t.Errorf("DiscardStagedPackage(%q) error = %v, want ErrStagingNotFound", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(env.stagingDir(), staged.ID)); err != nil {
		t.Fatalf("staged package disappeared: %v", err)
	}
}

func TestDiscardStagedPackageRemovesIt(t *testing.T) {
	env := newPublishEnv(t)
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	if err := DiscardStagedPackage(env.manifestPath, staged.ID); err != nil {
		t.Fatalf("DiscardStagedPackage() error = %v", err)
	}
	if _, err := StagedPackagePath(env.manifestPath, staged.ID, testNow, DefaultStagingTTL); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("StagedPackagePath() after discard error = %v, want ErrStagingNotFound", err)
	}
}

// 暂存后的预检与确认，结果与直接从文件发布完全一致：同样的返回值、同样的清单与文件。
func TestPublishStagedPackageMatchesDirectPublish(t *testing.T) {
	previous := testDesktopPackage(t, "linux", "stable", 6000000)
	data := testDesktopPackage(t, "linux", "stable", 6000001)

	direct := newPublishEnv(t)
	direct.mustPublish(t, previous, PublishOptions{})
	wantDry, err := direct.publish(t, direct.writePackage(t, "next.bin", data), stagedOptions(t, true, false))
	if err != nil {
		t.Fatal(err)
	}
	want, err := direct.publish(t, direct.writePackage(t, "next.bin", data), stagedOptions(t, false, false))
	if err != nil {
		t.Fatal(err)
	}

	staged := newPublishEnv(t)
	staged.mustPublish(t, previous, PublishOptions{})
	pkg := staged.stage(t, data, testNow)
	beforeDry := snapshotTree(t, filepath.Dir(staged.manifestPath))
	gotDry, err := PublishStagedPackage(staged.manifestPath, staged.filesDir, pkg.ID, stagedOptions(t, true, false), DefaultStagingTTL)
	if err != nil {
		t.Fatalf("PublishStagedPackage(dry run) error = %v", err)
	}
	if !reflect.DeepEqual(gotDry, wantDry) {
		t.Fatalf("staged dry run = %+v, want %+v", gotDry, wantDry)
	}
	assertTreeUnchanged(t, filepath.Dir(staged.manifestPath), beforeDry)

	got, err := PublishStagedPackage(staged.manifestPath, staged.filesDir, pkg.ID, stagedOptions(t, false, false), DefaultStagingTTL)
	if err != nil {
		t.Fatalf("PublishStagedPackage() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staged publish = %+v, want %+v", got, want)
	}
	wantManifest, _ := os.ReadFile(direct.manifestPath)
	gotManifest, _ := os.ReadFile(staged.manifestPath)
	if !bytes.Equal(gotManifest, wantManifest) {
		t.Fatalf("manifest after staged publish:\n%s\nwant:\n%s", gotManifest, wantManifest)
	}
	if !reflect.DeepEqual(snapshotTree(t, staged.filesDir), snapshotTree(t, direct.filesDir)) {
		t.Fatal("files directory differs between staged and direct publish")
	}
	if names := dirNames(t, staged.stagingDir()); len(names) != 0 {
		t.Fatalf("staging directory holds %v after a successful publish", names)
	}
}

func TestPublishStagedPackageReverifiesTheStagedFile(t *testing.T) {
	env := newPublishEnv(t)
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	if _, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, true, false), DefaultStagingTTL); err != nil {
		t.Fatal(err)
	}
	// 预检之后暂存文件被改动：确认时重新验签，不信任预检结果。
	path := filepath.Join(env.stagingDir(), staged.ID)
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, env.filesDir)
	if _, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, false, false), DefaultStagingTTL); err == nil {
		t.Fatal("PublishStagedPackage() accepted a package that changed after the preflight")
	}
	assertTreeUnchanged(t, env.filesDir, before)
	if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest written for a tampered package: %v", err)
	}
}

func TestPublishStagedPackageKeepsStagingWhenPublishFails(t *testing.T) {
	env := newPublishEnv(t)
	env.mustPublish(t, testDesktopPackage(t, "linux", "stable", 6000001), PublishOptions{})
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	_, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, false, false), DefaultStagingTTL)
	assertPublishError(t, err, "is not newer than the published build")
	// 运营者可以勾选“允许回滚”后用同一个暂存包重试，不必重新上传。
	result, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, false, true), DefaultStagingTTL)
	if err != nil || result.Release.Build != 6000000 {
		t.Fatalf("rollback with the same staging = %+v, %v", result, err)
	}
}

func TestPublishStagedPackageSharesTheManifestLock(t *testing.T) {
	env := newPublishEnv(t)
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	if err := os.WriteFile(env.manifestPath+".lock", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, false, false), DefaultStagingTTL)
	if !errors.Is(err, ErrPublishInProgress) {
		t.Fatalf("PublishStagedPackage() error = %v, want ErrPublishInProgress", err)
	}
	assertPublishError(t, err, "another publish is in progress")
}

func TestPublishStagedPackageReportsMissingAndExpired(t *testing.T) {
	env := newPublishEnv(t)
	_, err := PublishStagedPackage(env.manifestPath, env.filesDir, "0123456789abcdef0123456789abcdef", stagedOptions(t, true, false), DefaultStagingTTL)
	if !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("missing staging error = %v, want ErrStagingNotFound", err)
	}
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow.Add(-DefaultStagingTTL-time.Minute))
	// 清理先于查找运行，过期的暂存直接被删掉，查找时已不存在。
	_, err = PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, true, false), DefaultStagingTTL)
	if !errors.Is(err, ErrStagingNotFound) && !errors.Is(err, ErrStagingExpired) {
		t.Fatalf("expired staging error = %v, want ErrStagingExpired or ErrStagingNotFound", err)
	}
}

func TestHandlerDoesNotServeStagedPackages(t *testing.T) {
	env := newPublishEnv(t)
	env.mustPublish(t, testDesktopPackage(t, "linux", "stable", 6000000), PublishOptions{})
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000001), testNow)
	store, err := NewStore(env.manifestPath, env.filesDir, testCatalogOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/files/.staging/" + staged.ID, "/files/" + staged.ID, "/files/..%2F.staging%2F" + staged.ID} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// Store 的暂存方法使用 Store 自己的清单、文件目录与加载选项，发布后下一次快照即可见。
func TestStoreStagesAndPublishesIntoItsOwnManifest(t *testing.T) {
	env := newPublishEnv(t)
	env.mustPublish(t, testDesktopPackage(t, "linux", "stable", 6000000), PublishOptions{})
	store, err := NewStore(env.manifestPath, env.filesDir, testCatalogOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	staged, err := store.StagePackage(bytes.NewReader(testDesktopPackage(t, "linux", "stable", 6000001)))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PublishStagedPackage(staged.ID, true, false)
	if err != nil || preview.Release.Build != 6000001 || preview.Previous == nil || preview.Previous.Build != 6000000 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := store.PublishStagedPackage(staged.ID, false, false); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status()
	if err != nil || len(status.Desktop) != 1 || status.Desktop[0].Release.Build != 6000001 {
		t.Fatalf("status after publish = %+v, %v", status, err)
	}
	if err := store.DiscardStagedPackage(staged.ID); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("discard after publish = %v, want ErrStagingNotFound", err)
	}
}

// 读取上传失败（客户端断开、读取超时）与本机存储失败（磁盘满、建目录失败）分开报告：
// 前者是客户端的问题，后者是服务器的问题，调用方据此给出不同的错误码。
func TestStagePackageSeparatesReadAndStorageFailures(t *testing.T) {
	env := newPublishEnv(t)
	broken := io.MultiReader(bytes.NewReader(make([]byte, 4096)), errReader{errors.New("connection reset")})
	_, err := StagePackage(env.manifestPath, broken, testNow, DefaultStagingTTL)
	if !errors.Is(err, ErrStagingReadFailed) || errors.Is(err, ErrStagingStorage) {
		t.Fatalf("read failure error = %v, want ErrStagingReadFailed only", err)
	}

	blocked := newPublishEnv(t)
	// 暂存目录的位置被一个普通文件占着，建不了目录。
	if err := os.WriteFile(blocked.stagingDir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = StagePackage(blocked.manifestPath, bytes.NewReader([]byte("package")), testNow, DefaultStagingTTL)
	if !errors.Is(err, ErrStagingStorage) || errors.Is(err, ErrStagingReadFailed) {
		t.Fatalf("storage failure error = %v, want ErrStagingStorage only", err)
	}
}

// setStagingCaps 临时调整同时暂存的数量与总字节上限。
func setStagingCaps(t *testing.T, count int, total int64) {
	t.Helper()
	previousCount, previousTotal := stagingMaxPending, stagingMaxPendingBytes
	stagingMaxPending, stagingMaxPendingBytes = count, total
	t.Cleanup(func() { stagingMaxPending, stagingMaxPendingBytes = previousCount, previousTotal })
}

func TestStagePackageRefusesWhenTooManyArePending(t *testing.T) {
	env := newPublishEnv(t)
	setStagingCaps(t, 2, 1<<30)
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	env.stage(t, data, testNow.Add(-time.Minute))
	env.stage(t, data, testNow)
	reader := &countingReader{r: bytes.NewReader(data)}
	_, err := StagePackage(env.manifestPath, reader, testNow, DefaultStagingTTL)
	if !errors.Is(err, ErrStagingFull) {
		t.Fatalf("third staging error = %v, want ErrStagingFull", err)
	}
	if reader.n != 0 {
		t.Fatalf("read %d bytes of a refused upload, want none", reader.n)
	}
	if names := dirNames(t, env.stagingDir()); len(names) != 2 {
		t.Fatalf("staging directory = %v, want the two earlier stagings", names)
	}
	// 过期清理先于上限检查：最早的暂存过期后又能上传。
	later := testNow.Add(DefaultStagingTTL - 30*time.Second)
	if _, err := StagePackage(env.manifestPath, bytes.NewReader(data), later, DefaultStagingTTL); err != nil {
		t.Fatalf("staging after one expired error = %v", err)
	}
}

func TestStagePackageRefusesWhenPendingBytesReachTheCap(t *testing.T) {
	env := newPublishEnv(t)
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	setStagingCaps(t, 100, int64(2*len(data)))
	env.stage(t, data, testNow)
	env.stage(t, data, testNow)
	if _, err := StagePackage(env.manifestPath, bytes.NewReader(data), testNow, DefaultStagingTTL); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("staging past the byte cap error = %v, want ErrStagingFull", err)
	}
}

// 未完成的上传（.part）同样占名额。
func TestStagePackageCountsUnfinishedUploads(t *testing.T) {
	env := newPublishEnv(t)
	setStagingCaps(t, 1, 1<<30)
	if err := os.MkdirAll(env.stagingDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(env.stagingDir(), "0123456789abcdef0123456789abcdef.part")
	if err := os.WriteFile(part, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(part, testNow, testNow); err != nil {
		t.Fatal(err)
	}
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	if _, err := StagePackage(env.manifestPath, bytes.NewReader(data), testNow, DefaultStagingTTL); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("staging beside an unfinished upload error = %v, want ErrStagingFull", err)
	}
}

// 同一个暂存 id 的两次确认并发：只发布一次，另一次报告暂存不存在。
func TestPublishStagedPackageConcurrentConfirmsPublishOnce(t *testing.T) {
	data := testDesktopPackage(t, "linux", "stable", 6000000)
	for i := 0; i < 20; i++ {
		env := newPublishEnv(t)
		staged := env.stage(t, data, testNow)
		opts := stagedOptions(t, false, false)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				_, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, opts, DefaultStagingTTL)
				errs <- err
			}()
		}
		close(start)
		var published, notFound int
		for range 2 {
			switch err := <-errs; {
			case err == nil:
				published++
			case errors.Is(err, ErrStagingNotFound):
				notFound++
			default:
				t.Fatalf("concurrent confirm error = %v, want success or ErrStagingNotFound", err)
			}
		}
		if published != 1 || notFound != 1 {
			t.Fatalf("concurrent confirms: %d published, %d not found; want exactly one of each", published, notFound)
		}
		if names := dirNames(t, env.stagingDir()); len(names) != 0 {
			t.Fatalf("staging directory holds %v after the publish", names)
		}
	}
}

// 认领后发布失败，暂存恢复原名，可以重试。
func TestPublishStagedPackageRestoresClaimWhenPublishFails(t *testing.T) {
	env := newPublishEnv(t)
	staged := env.stage(t, testDesktopPackage(t, "linux", "stable", 6000000), testNow)
	if err := os.WriteFile(env.manifestPath+".lock", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, false, false), DefaultStagingTTL); !errors.Is(err, ErrPublishInProgress) {
		t.Fatalf("locked publish error = %v, want ErrPublishInProgress", err)
	}
	if names := dirNames(t, env.stagingDir()); !reflect.DeepEqual(names, []string{staged.ID}) {
		t.Fatalf("staging directory = %v, want the staging restored as %s", names, staged.ID)
	}
	if err := os.Remove(env.manifestPath + ".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishStagedPackage(env.manifestPath, env.filesDir, staged.ID, stagedOptions(t, false, false), DefaultStagingTTL); err != nil {
		t.Fatalf("retry after the lock cleared error = %v", err)
	}
}

// 发布中的认领文件不按普通有效期清理（发布可能正在读它），超过两倍有效期才算残留。
func TestCleanupStagedPackagesKeepsRecentClaims(t *testing.T) {
	env := newPublishEnv(t)
	if err := os.MkdirAll(env.stagingDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(env.stagingDir(), "0123456789abcdef0123456789abcdef.claim")
	stale := filepath.Join(env.stagingDir(), "fedcba9876543210fedcba9876543210.claim")
	for path, age := range map[string]time.Duration{recent: DefaultStagingTTL + time.Minute, stale: 2*DefaultStagingTTL + time.Minute} {
		if err := os.WriteFile(path, []byte("claimed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, testNow.Add(-age), testNow.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := CleanupStagedPackages(env.manifestPath, testNow, DefaultStagingTTL); err != nil || removed != 1 {
		t.Fatalf("CleanupStagedPackages() = %d, %v; want the stale claim removed", removed, err)
	}
	if names := dirNames(t, env.stagingDir()); !reflect.DeepEqual(names, []string{filepath.Base(recent)}) {
		t.Fatalf("staging directory = %v, want only the recent claim", names)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
