package postgres

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"telesrv/deploy"
	"telesrv/internal/store/postgres/pgtest"
)

// knownMigrationGaps 是历史遗留的空号：0123 从来没有存在过。golang-migrate 只保存一个
// schema_migrations.version，凡是低于它的迁移都不会再被执行，所以新开一个空号等于把
// 那条迁移永久丢在门外——除这份明示清单以外的任何空号都是事故。
var knownMigrationGaps = map[uint]struct{}{123: {}}

// embeddedMigrationVersions 以 deploy.Migrations 为唯一事实来源，按后缀取出版本号到文
// 件名的映射，顺手钉住重号。
func embeddedMigrationVersions(t *testing.T, suffix string) map[uint]string {
	t.Helper()

	entries, err := deploy.Migrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	out := make(map[uint]string, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}

		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %q has no version prefix", name)
		}

		version, err := strconv.ParseUint(prefix, 10, strconv.IntSize)
		if err != nil {
			t.Fatalf("migration %q has a non-numeric version prefix: %v", name, err)
		}

		if previous, duplicate := out[uint(version)]; duplicate {
			t.Fatalf("migrations %q and %q share version %d", previous, name, version)
		}

		out[uint(version)] = name
	}

	if len(out) == 0 {
		t.Fatalf("no embedded %s migrations found", suffix)
	}

	return out
}

// headMigrationVersion 从嵌入的迁移脚本推导期望版本号。硬编码版本号每加一条迁移
// 就会失效（本用例曾长期停留在 185），因此以 deploy.Migrations 为唯一事实来源。
func headMigrationVersion(t *testing.T) uint {
	t.Helper()

	var head uint
	for version := range embeddedMigrationVersions(t, ".up.sql") {
		if version > head {
			head = version
		}
	}

	return head
}

// 只看最大文件名的检查会漏掉真正危险的形态：中间留一个空号，等着以后补。真实部署一
// 旦跑到后面的版本，补进来的那条就永远低于 schema_migrations.version，既不会执行也不
// 会报错。所以这里钉的是整条序列——不重号、每条 up 都有 down、逐一递增。
func TestEmbeddedMigrationsAreContiguousAndReversible(t *testing.T) {
	ups := embeddedMigrationVersions(t, ".up.sql")
	downs := embeddedMigrationVersions(t, ".down.sql")

	versions := make([]uint, 0, len(ups))
	for version := range ups {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	if versions[0] != 1 {
		t.Fatalf("migration series starts at %q, want version 1", ups[versions[0]])
	}

	// 编号链在 lastNumberedMigration 结束，之后改用时间戳版本。时间戳之间本来就不连续，
	// 逐号检查不适用；改为钉住"已发布"的有序前缀：实际的时间戳序列必须以它开头，
	// 新加的迁移只能排在它最后一项之后。追加迁移不用改这里；把一条插到已发布版本之间
	// （部署过的库永远不会执行它）会让前缀对不上而失败。
	// 想把新版本也钉住时，按顺序追加到 publishedTimestampVersions 末尾即可。
	const lastNumberedMigration = uint(223)
	publishedTimestampVersions := []uint{
		20260924140000,
		20260924150000,
		20260926010000,
		20260927010000,
		20260928010000,
		20260928020000,
		20260928030000,
		20261012010000,
		20261013010000,
		20261015010000,
		20261016010000,
		20261017010000,
		20261018010000,
		20261019010000,
		20261020010000,
		20261021010000,
		20261021020000,
		20261022010000,
	}
	numbered := versions
	var timestamps []uint
	for index, version := range versions {
		if version > lastNumberedMigration {
			numbered, timestamps = versions[:index], versions[index:]
			break
		}
	}
	// versions[0] == 1 已检查，numbered 非空。第一条时间戳版本的前驱必须是 223。
	if last := numbered[len(numbered)-1]; last != lastNumberedMigration {
		t.Fatalf("numbered migration chain ends at %d, want %d right before the first timestamp version", last, lastNumberedMigration)
	}
	if len(timestamps) < len(publishedTimestampVersions) {
		t.Fatalf("timestamp migrations = %v, want the published prefix %v", timestamps, publishedTimestampVersions)
	}
	for index, want := range publishedTimestampVersions {
		if timestamps[index] != want {
			t.Fatalf("timestamp migration #%d = %q (%d), want published version %d: a migration was inserted "+
				"among or removed from the published versions; deployed databases would never run it",
				index, ups[timestamps[index]], timestamps[index], want)
		}
	}
	lastPublished := publishedTimestampVersions[len(publishedTimestampVersions)-1]
	for _, version := range timestamps[len(publishedTimestampVersions):] {
		if version <= lastPublished {
			t.Fatalf("new migration %q must sort after the last published version %d", ups[version], lastPublished)
		}
	}

	for index, version := range versions {
		if _, ok := downs[version]; !ok {
			t.Fatalf("migration %q has no down script", ups[version])
		}

		if index == 0 || version > lastNumberedMigration {
			continue
		}

		previous := versions[index-1]
		for missing := previous + 1; missing < version; missing++ {
			if _, allowed := knownMigrationGaps[missing]; allowed {
				continue
			}

			t.Fatalf("migration series skips %04d between %q and %q: golang-migrate keeps a single "+
				"schema_migrations.version, so a migration landed there later would never run",
				missing, ups[previous], ups[version])
		}
	}

	for version, name := range downs {
		if _, ok := ups[version]; !ok {
			t.Fatalf("down migration %q has no up script", name)
		}
	}

	for gap := range knownMigrationGaps {
		if name, filled := ups[gap]; filled {
			t.Fatalf("migration %q filled historic gap %04d: drop it from knownMigrationGaps", name, gap)
		}
	}
}

// "从空库迁移到最新"由 pgtest 承担：模板库按迁移内容哈希命名，迁移内容一变就新建一个，
// 从空库跑 MigrateAndStatus 到 head 并断言版本，失败则本包所有 Postgres 测试都拿不到库。
// 这里核对本包的克隆库（模板的副本）停在 head 且不脏，再迁移一次也只是空操作。
func TestStarGiftLifecycleMigrationsApply(t *testing.T) {
	pool := testPool(t)
	head := headMigrationVersion(t)
	if pgHead, err := pgtest.HeadVersion(); err != nil || pgHead != head {
		t.Fatalf("pgtest head = %d (%v), want %d", pgHead, err, head)
	}

	var version int64
	var dirty bool
	if err := pool.QueryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if dirty || version != int64(head) {
		t.Fatalf("cloned schema version = %d (dirty=%v), want clean version %d", version, dirty, head)
	}

	status, err := MigrateAndStatus(pgtest.DSN(t))
	if err != nil {
		t.Fatalf("re-migrate cloned schema: %v", err)
	}
	if status.Dirty || status.Empty || status.Version != head {
		t.Fatalf("migration status = %+v, want clean version %d", status, head)
	}
}
