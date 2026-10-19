// Package pgtest 是 Postgres 集成测试的共享基础设施：模板库 + 克隆库。
//
// 旧做法是每个测试都对 TELESRV_TEST_POSTGRES_DSN 指向的同一个库跑一遍迁移，所有包共用
// 一个库：跨包 TRUNCATE、全局认领、余额累计互相污染，新库首次还要跑 200 多个迁移。
// 这里改为：
//
//   - 模板库 telesrv_tmpl_<迁移哈希>_test：用嵌入迁移（deploy.Migrations）的内容哈希命名，
//     从空库迁移到 head 并断言版本，然后设为 IS_TEMPLATE、禁止连接。哈希不变就复用，
//     多个测试进程并发时由维护连接上的 advisory lock 串行化。
//   - 包级克隆库 <prefix>_<rand>_test：每个测试二进制（每个包）一个，DSN(t) 取得，
//     Run 结束时 DROP ... WITH (FORCE)。
//   - 按测试克隆库：NewDatabase(t) 给依赖全局状态（全表计数、全局认领、TRUNCATE）的测试
//     独占一个库，t.Cleanup 时删除。
//
// 本包不 import internal/store/postgres（否则 postgres 包内测试会成环），迁移函数由调用方
// 在 Options.Migrate 里传入，通常就是 postgres.Migrate。
//
// 用法：
//
//	func TestMain(m *testing.M) {
//		os.Exit(pgtest.Run(m, pgtest.Options{Prefix: "rpc", Migrate: postgres.Migrate}))
//	}
//
//	func TestX(t *testing.T) {
//		pool, err := postgres.Open(ctx, pgtest.DSN(t)) // 包级共享库；未设 DSN 时 Skip
//		...
//	}
//
//	func TestNeedsWholeDatabase(t *testing.T) {
//		dsn := pgtest.NewDatabase(t) // 本测试独占的库
//		...
//	}
//
// 测试账号需要 CREATEDB 权限（CI 的 postgres service 用户是超级用户）。
package pgtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/deploy"
)

// EnvDSN 指向一个专用测试库（库名必须含 "test"）。它只作为连接参数与维护入口：
// 测试数据写进由它克隆出来的库，不写进它本身。
const EnvDSN = "TELESRV_TEST_POSTGRES_DSN"

// templateLockKey 串行化多个测试进程创建同一模板库（"pgtmpl" 的 ASCII）。
const templateLockKey = int64(0x7067746d706c)

const opTimeout = 5 * time.Minute

// Options 配置一个测试二进制（一个包）的数据库。
type Options struct {
	// Prefix 是克隆库名前缀，只保留 [a-z0-9_]，截断到 20 字符；空时为 "pkg"。
	Prefix string
	// Migrate 把空库迁移到最新版本，通常传 postgres.Migrate。只在新建模板库时调用。
	Migrate func(dsn string) error
	// ExportEnv 为 true 时 Run 在 m.Run 之前就建好包级克隆库，并把 EnvDSN 改写成它的 DSN，
	// 让直接读环境变量的旧测试也落到克隆库里。原始 DSN 仍由 BaseDSN 返回。
	ExportEnv bool
}

var state struct {
	mu         sync.Mutex
	registered bool
	opts       Options
	base       string // 原始 EnvDSN
	extra      map[string]struct{}

	tmplOnce sync.Once
	tmplName string
	tmplErr  error

	pkgOnce sync.Once
	pkgName string
	pkgDSN  string
	pkgErr  error
}

// Run 在 TestMain 中调用：登记选项、运行测试、删除本进程创建的克隆库。返回值交给 os.Exit。
// 未设置 EnvDSN 时只运行测试；需要库的测试会在 DSN/NewDatabase 里 Skip。
func Run(m *testing.M, opts Options) int {
	if opts.Migrate == nil {
		panic("pgtest: Options.Migrate is required")
	}
	state.mu.Lock()
	state.registered = true
	state.opts = opts
	state.base = strings.TrimSpace(os.Getenv(EnvDSN))
	state.extra = make(map[string]struct{})
	state.mu.Unlock()

	if opts.ExportEnv && state.base != "" {
		if dsn, err := packageDatabase(); err == nil {
			_ = os.Setenv(EnvDSN, dsn)
		} else {
			// 不在这里退出：不需要库的测试照常跑，需要库的测试在 DSN(t) 里报这个错。
			fmt.Fprintf(os.Stderr, "pgtest: %v\n", err)
		}
	}

	code := m.Run()

	if state.base != "" {
		state.mu.Lock()
		names := make([]string, 0, len(state.extra)+1)
		for name := range state.extra {
			names = append(names, name)
		}
		if state.pkgName != "" {
			names = append(names, state.pkgName)
		}
		state.mu.Unlock()
		for _, name := range names {
			if err := dropDatabase(name); err != nil {
				fmt.Fprintf(os.Stderr, "pgtest: drop %s: %v\n", name, err)
			}
		}
	}
	return code
}

// BaseDSN 返回原始的 EnvDSN（ExportEnv 改写环境变量之前的值）；未设置时 Skip。
func BaseDSN(tb testing.TB) string {
	tb.Helper()
	requireConfigured(tb)
	return state.base
}

// DSN 返回本包共享克隆库的 DSN；第一次调用时创建（ExportEnv 时已在 Run 中创建）。
// 未设置 EnvDSN 时 Skip。同包测试共享这个库，数据要么用唯一 id，要么自己清理。
func DSN(tb testing.TB) string {
	tb.Helper()
	requireConfigured(tb)
	dsn, err := packageDatabase()
	if err != nil {
		tb.Fatalf("pgtest: %v", err)
	}
	return dsn
}

// NewDatabase 为当前测试克隆一个独占库并返回其 DSN，测试结束时删除（WITH FORCE，
// 测试里没关的连接也会被断开）。给依赖全局状态的测试用：全表计数、全局认领、TRUNCATE。
// 未设置 EnvDSN 时 Skip。
func NewDatabase(tb testing.TB) string {
	tb.Helper()
	requireConfigured(tb)
	tmpl, err := templateDatabase()
	if err != nil {
		tb.Fatalf("pgtest: %v", err)
	}
	name, err := cloneName(state.opts.Prefix + "_t")
	if err != nil {
		tb.Fatalf("pgtest: %v", err)
	}
	if err := createClone(name, tmpl); err != nil {
		tb.Fatalf("pgtest: %v", err)
	}
	state.mu.Lock()
	state.extra[name] = struct{}{}
	state.mu.Unlock()
	tb.Cleanup(func() {
		if err := dropDatabase(name); err != nil {
			tb.Errorf("pgtest: drop %s: %v", name, err)
			return
		}
		state.mu.Lock()
		delete(state.extra, name)
		state.mu.Unlock()
	})
	dsn, err := withDatabase(state.base, name)
	if err != nil {
		tb.Fatalf("pgtest: %v", err)
	}
	return dsn
}

// perTestLimit 限制同时存活的按测试克隆库数量：每个库都有连接池和连接准入控制器的长连接，
// 并行测试全开会耗尽服务器的 max_connections（默认 100）。PGTEST_PARALLEL_DBS 可覆盖。
var perTestLimit = func() chan struct{} {
	n := 8
	if v, err := strconv.Atoi(os.Getenv("PGTEST_PARALLEL_DBS")); err == nil && v > 0 {
		n = v
	}
	return make(chan struct{}, n)
}()

var perTest struct {
	mu  sync.Mutex
	dsn map[string]string // 测试名 -> 该测试独占库的 DSN
}

// TestDSN 返回当前测试独占的克隆库 DSN，并让顶层测试并行运行：库是这个测试独占的，
// 全表计数、TRUNCATE、全局认领都不会互相影响，因此不需要逐个审查测试才能并行。
//
// 同一个测试（含其子测试）多次调用返回同一个库，模拟重启的"再开一个连接池"仍然看到同样的数据。
// 第一次调用发生在子测试里时，库归该子测试所有，且不调用 Parallel（子测试的 Parallel 会让父测试
// 先于它返回，改变语义）。测试结束时删库。未设置 EnvDSN 时 Skip。
//
// 依赖整个服务器状态（max_connections、pg_stat_activity）的测试不要用它，留在串行的包级库上。
func TestDSN(t *testing.T) string {
	t.Helper()
	requireConfigured(t)
	name := t.Name()
	top, _, isSub := strings.Cut(name, "/")
	perTest.mu.Lock()
	if perTest.dsn == nil {
		perTest.dsn = make(map[string]string)
	}
	if dsn, ok := perTest.dsn[name]; ok {
		perTest.mu.Unlock()
		return dsn
	}
	if dsn, ok := perTest.dsn[top]; ok && isSub {
		perTest.mu.Unlock()
		return dsn
	}
	perTest.mu.Unlock()

	if !isSub {
		t.Parallel()
	}
	perTestLimit <- struct{}{}
	t.Cleanup(func() { <-perTestLimit })
	dsn := NewDatabase(t)
	perTest.mu.Lock()
	perTest.dsn[name] = dsn
	perTest.mu.Unlock()
	t.Cleanup(func() {
		perTest.mu.Lock()
		delete(perTest.dsn, name)
		perTest.mu.Unlock()
	})
	return dsn
}

// TemplateName 返回当前嵌入迁移对应的模板库名：telesrv_tmpl_<hash>_test。
func TemplateName() (string, error) {
	hash, err := migrationsHash()
	if err != nil {
		return "", err
	}
	return "telesrv_tmpl_" + hash + "_test", nil
}

// HeadVersion 返回嵌入迁移的最大 up 版本号，模板库迁移后必须恰好停在这里。
func HeadVersion() (uint, error) {
	entries, err := fs.ReadDir(deploy.Migrations, "migrations")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	var head uint
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("migration %q has no version prefix", name)
		}
		version, err := strconv.ParseUint(prefix, 10, strconv.IntSize)
		if err != nil {
			return 0, fmt.Errorf("migration %q: %w", name, err)
		}
		head = max(head, uint(version))
	}
	if head == 0 {
		return 0, errors.New("no embedded up migrations")
	}
	return head, nil
}

func requireConfigured(tb testing.TB) {
	tb.Helper()
	state.mu.Lock()
	registered, base := state.registered, state.base
	state.mu.Unlock()
	if !registered {
		if os.Getenv(EnvDSN) == "" {
			tb.Skip("set " + EnvDSN + " to run postgres integration test")
		}
		tb.Fatal("pgtest: call pgtest.Run from this package's TestMain")
	}
	if base == "" {
		tb.Skip("set " + EnvDSN + " to run postgres integration test")
	}
}

func packageDatabase() (string, error) {
	state.pkgOnce.Do(func() {
		tmpl, err := templateDatabase()
		if err != nil {
			state.pkgErr = err
			return
		}
		name, err := cloneName(state.opts.Prefix)
		if err != nil {
			state.pkgErr = err
			return
		}
		if err := createClone(name, tmpl); err != nil {
			state.pkgErr = err
			return
		}
		state.mu.Lock()
		state.pkgName = name
		state.mu.Unlock()
		state.pkgDSN, state.pkgErr = withDatabase(state.base, name)
	})
	return state.pkgDSN, state.pkgErr
}

func templateDatabase() (string, error) {
	state.tmplOnce.Do(func() {
		state.tmplName, state.tmplErr = ensureTemplate()
	})
	return state.tmplName, state.tmplErr
}

func ensureTemplate() (string, error) {
	cfg, err := pgx.ParseConfig(state.base)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", EnvDSN, err)
	}
	if !strings.Contains(strings.ToLower(cfg.Database), "test") {
		return "", fmt.Errorf("%s must name a dedicated test database, got %q", EnvDSN, cfg.Database)
	}
	name, err := TemplateName()
	if err != nil {
		return "", err
	}
	head, err := HeadVersion()
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	conn, err := maintenanceConn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close(context.Background())
	// 其他测试进程可能正在建同一个模板：拿到锁后再看它是否已就绪。
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, templateLockKey); err != nil {
		return "", fmt.Errorf("lock template creation: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, templateLockKey)
	}()

	var isTemplate bool
	err = conn.QueryRow(ctx, `SELECT datistemplate FROM pg_database WHERE datname = $1`, name).Scan(&isTemplate)
	switch {
	case err == nil && isTemplate:
		return name, nil
	case err == nil:
		// 持锁时还不是模板，只能是上次建到一半的残骸。
		if _, err := conn.Exec(ctx, `DROP DATABASE `+ident(name)+` WITH (FORCE)`); err != nil {
			return "", fmt.Errorf("drop unfinished template %s: %w", name, err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("look up template %s: %w", name, err)
	}

	if _, err := conn.Exec(ctx, `CREATE DATABASE `+ident(name)+` TEMPLATE template0`); err != nil {
		return "", fmt.Errorf("create template %s: %w", name, err)
	}
	fail := func(err error) (string, error) {
		_, _ = conn.Exec(context.Background(), `DROP DATABASE IF EXISTS `+ident(name)+` WITH (FORCE)`)
		return "", err
	}
	dsn, err := withDatabase(state.base, name)
	if err != nil {
		return fail(err)
	}
	// 从空库迁移到 head：这一步同时证明整条迁移链能在空库上跑通。
	if err := state.opts.Migrate(dsn); err != nil {
		return fail(fmt.Errorf("migrate template %s: %w", name, err))
	}
	if err := verifyHead(ctx, dsn, head); err != nil {
		return fail(fmt.Errorf("template %s: %w", name, err))
	}
	if _, err := conn.Exec(ctx, `ALTER DATABASE `+ident(name)+` WITH ALLOW_CONNECTIONS false IS_TEMPLATE true`); err != nil {
		return fail(fmt.Errorf("mark template %s: %w", name, err))
	}
	// 迁移器可能留下尚未退出的后端；有连接时无法以它为模板建库。
	if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return fail(fmt.Errorf("disconnect template %s: %w", name, err))
	}
	return name, nil
}

func verifyHead(ctx context.Context, dsn string, head uint) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())
	var version int64
	var dirty bool
	if err := conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	if dirty || version != int64(head) {
		return fmt.Errorf("migrated to version %d (dirty=%v), want clean head %d", version, dirty, head)
	}
	return nil
}

func createClone(name, tmpl string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	conn, err := maintenanceConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+ident(name)+` TEMPLATE `+ident(tmpl)); err != nil {
		return fmt.Errorf("clone %s from %s: %w", name, tmpl, err)
	}
	return nil
}

func dropDatabase(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	conn, err := maintenanceConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+ident(name)+` WITH (FORCE)`)
	return err
}

// maintenanceConn 连到 postgres 库做 CREATE/DROP DATABASE；连不上（没有该库的权限）时
// 退回 EnvDSN 指向的库本身。
func maintenanceConn(ctx context.Context) (*pgx.Conn, error) {
	dsn, err := withDatabase(state.base, "postgres")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err == nil {
		return conn, nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil, fmt.Errorf("connect maintenance database: %w", err)
	}
	conn, err = pgx.Connect(ctx, state.base)
	if err != nil {
		return nil, fmt.Errorf("connect maintenance database: %w", err)
	}
	return conn, nil
}

func migrationsHash() (string, error) {
	entries, err := fs.ReadDir(deploy.Migrations, "migrations")
	if err != nil {
		return "", fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		body, err := fs.ReadFile(deploy.Migrations, "migrations/"+name)
		if err != nil {
			return "", fmt.Errorf("read migration %s: %w", name, err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(body))
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

var nonIdent = regexp.MustCompile(`[^a-z0-9_]+`)

func cloneName(prefix string) (string, error) {
	prefix = nonIdent.ReplaceAllString(strings.ToLower(prefix), "_")
	prefix = strings.Trim(prefix, "_")
	if prefix == "" {
		prefix = "pkg"
	}
	if len(prefix) > 20 {
		prefix = prefix[:20]
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random database name: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]) + "_test", nil
}

// withDatabase 把 DSN 的库名换成 name；支持 URL 形式与 key=value 形式。
func withDatabase(dsn, name string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", EnvDSN, err)
		}
		u.Path = "/" + name
		u.RawPath = ""
		return u.String(), nil
	}
	// key=value 形式里后出现的同名参数覆盖前面的。
	return dsn + " dbname=" + name, nil
}

func ident(name string) string {
	return pgx.Identifier{name}.Sanitize()
}
