package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	applangpack "telesrv/internal/app/langpack"
	"telesrv/internal/domain"
)

func TestLangPackSeedReconciliationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	packName := "seedtest-" + randomSuffix(t)
	store := NewLangPackStore(pool)

	seedV1 := domain.LangPackSeed{
		Catalog: packName,
		Scopes:  []string{packName},
		Packs: []domain.LangPackSeedEntry{{
			SourceHash:    "source-v1",
			ContentHash:   "hash-v1",
			StringsCount:  2,
			ContentLoaded: true,
			Pack: domain.LangPack{
				LangPack: packName,
				LangCode: "fr",
				Version:  1,
				Strings: []domain.LangPackString{
					{Key: "LanguageName", Value: "Français"},
					{Key: "old", Value: "old"},
				},
			},
		}},
	}
	written, err := store.ReconcileSeed(ctx, seedV1)
	if err != nil || written != 2 {
		t.Fatalf("reconcile v1 = %d, %v", written, err)
	}
	seedV1Unloaded := seedV1
	seedV1Unloaded.Packs = append([]domain.LangPackSeedEntry(nil), seedV1.Packs...)
	seedV1Unloaded.Packs[0].Pack.Strings = nil
	seedV1Unloaded.Packs[0].ContentLoaded = false
	if written, err := store.ReconcileSeed(ctx, seedV1Unloaded); err != nil || written != 0 {
		t.Fatalf("reconcile unchanged v1 = %d, %v", written, err)
	}
	catalog, err := store.GetSeedCatalog(ctx, packName)
	if err != nil || len(catalog.Packs) != 1 || catalog.Packs[0].SourceHash != "source-v1" {
		t.Fatalf("seed catalog = %+v, %v", catalog, err)
	}

	seedV2 := domain.LangPackSeed{
		Catalog: packName,
		Scopes:  []string{packName},
		Packs: []domain.LangPackSeedEntry{{
			SourceHash:    "source-v2",
			ContentHash:   "hash-v2",
			StringsCount:  2,
			ContentLoaded: true,
			Pack: domain.LangPack{
				LangPack: packName,
				LangCode: "fr",
				Version:  2,
				Strings: []domain.LangPackString{
					{Key: "LanguageName", Value: "Français v2"},
					{Key: "new", Value: "new"},
				},
			},
		}},
	}
	written, err = store.ReconcileSeed(ctx, seedV2)
	if err != nil || written != 2 {
		t.Fatalf("reconcile v2 = %d, %v", written, err)
	}
	pack, err := store.GetPack(ctx, packName, "fr", 0)
	if err != nil || pack.Version != 2 || len(pack.Strings) != 2 || postgresLangPackValue(pack.Strings, "old") != "" || postgresLangPackValue(pack.Strings, "new") != "new" {
		t.Fatalf("replaced postgres pack = %+v, err %v", pack, err)
	}

	mutated := seedV2
	mutated.Packs = append([]domain.LangPackSeedEntry(nil), seedV2.Packs...)
	mutated.Packs[0].ContentHash = "changed-without-version"
	if _, err := store.ReconcileSeed(ctx, mutated); err == nil || !strings.Contains(err.Error(), "without version bump") {
		t.Fatalf("same-version mutation error = %v", err)
	}
	rollback := seedV2
	rollback.Packs = append([]domain.LangPackSeedEntry(nil), seedV2.Packs...)
	rollback.Packs[0].Pack.Version = 1
	rollback.Packs[0].ContentHash = "rollback"
	if n, err := store.ReconcileSeed(ctx, rollback); err != nil || n != 0 {
		t.Fatalf("older seed reconcile = %d, %v", n, err)
	}
	keptPack, keptErr := store.GetPack(ctx, packName, "fr", 0)
	if keptErr != nil || keptPack.Version != 2 || len(keptPack.Strings) != 2 {
		t.Fatalf("pack regressed after older seed = %+v, err %v", keptPack, keptErr)
	}

	if written, err := store.ReconcileSeed(ctx, domain.LangPackSeed{Catalog: packName}); err != nil || written != 0 {
		t.Fatalf("remove missing language = %d, %v", written, err)
	}
	languages, err := store.ListLanguages(ctx, packName)
	if err != nil || len(languages) != 0 {
		t.Fatalf("languages after manifest removal = %+v, err %v", languages, err)
	}
}

func postgresLangPackValue(items []domain.LangPackString, key string) string {
	for _, item := range items {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}

// TestBundledLangPackSeedPostgres 把真实的内置语言包写进 PostgreSQL：每个客户端目录取最小的
// 一个文件（各平台的 .strings 方言都覆盖到），断言首遍写入的条数等于解析结果、原样重放写入
// 0 条、元数据计数与有效行一致。完整的 113 MB 语料由 app/langpack 的内存测试覆盖，这里只验证
// PostgreSQL 一侧，不必把全部文件（26 秒）再灌一遍。
func TestBundledLangPackSeedPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	bundled := filepath.Join("..", "..", "..", "data", "langpack")
	root := t.TempDir()
	var scopes []string
	want := 0
	dirs, err := os.ReadDir(bundled)
	if err != nil {
		t.Fatalf("read bundled langpacks: %v", err)
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		smallest := smallestLangPackFile(t, filepath.Join(bundled, dir.Name()))
		if smallest == "" {
			continue
		}
		dst := filepath.Join(root, dir.Name(), filepath.Base(smallest))
		copyTestFile(t, smallest, dst)
		pack, err := applangpack.ParseTDesktopFile(dst)
		if err != nil {
			t.Fatalf("parse %s: %v", dst, err)
		}
		scopes = append(scopes, pack.LangPack)
		want += len(pack.Strings)
	}
	if len(scopes) < 5 || want == 0 {
		t.Fatalf("sampled %d bundled packs with %d strings, want every client directory", len(scopes), want)
	}

	service := applangpack.NewService(NewLangPackStore(pool))
	started := time.Now()
	seeded, err := service.SeedDirectory(ctx, root)
	firstDuration := time.Since(started)
	if err != nil {
		t.Fatalf("seed bundled langpacks to postgres: %v", err)
	}
	if seeded != want {
		t.Fatalf("seeded bundled strings = %d, want %d parsed from the sampled files", seeded, want)
	}

	started = time.Now()
	seededAgain, err := service.SeedDirectory(ctx, root)
	secondDuration := time.Since(started)
	if err != nil || seededAgain != 0 {
		t.Fatalf("reconcile unchanged bundled langpacks = %d, %v", seededAgain, err)
	}
	var metadataCount, activeCount int64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT COALESCE(sum(strings_count), 0) FROM lang_packs WHERE lang_pack = ANY($1::text[])),
  (SELECT count(*) FROM lang_pack_strings WHERE lang_pack = ANY($1::text[]) AND NOT deleted)
`, scopes).Scan(&metadataCount, &activeCount); err != nil {
		t.Fatalf("count seeded langpack rows: %v", err)
	}
	if metadataCount != activeCount || metadataCount != int64(want) {
		t.Fatalf("seeded strings_count = %d, active rows = %d, want %d", metadataCount, activeCount, want)
	}
	t.Logf("bundled langpack seed: first=%s unchanged=%s strings=%d", firstDuration, secondDuration, metadataCount)
}

func smallestLangPackFile(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.strings"))
	if err != nil {
		t.Fatal(err)
	}
	best, bestSize := "", int64(-1)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if bestSize < 0 || info.Size() < bestSize {
			best, bestSize = f, info.Size()
		}
	}
	return best
}

func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
