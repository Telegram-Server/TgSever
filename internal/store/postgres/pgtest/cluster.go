package pgtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnvHome 是私有集群的工作目录：PostgreSQL 二进制（dist/）、迁移快照（snapshots/）以及
// 没有内存盘时运行中的集群（run/）。默认是模块根目录下的 .pgtest/（已在 .gitignore 中），
// 不写系统盘的用户缓存或临时目录。
const EnvHome = "TELESRV_TEST_POSTGRES_HOME"

// EnvSkip 设为非空时不启动私有集群，需要 PostgreSQL 的测试直接 Skip：只想跑纯内存测试时用。
const EnvSkip = "TELESRV_TEST_POSTGRES_SKIP"

const (
	clusterUser     = "postgres"
	clusterDatabase = "telesrv_test"
	// staleLock 之内没有刷新的构建锁视为被杀进程的残留。
	staleLock = 2 * time.Minute
	// snapshotTTL 之内没用过的旧快照（迁移已变化）在建新快照后删除。
	snapshotTTL = 7 * 24 * time.Hour
	// snapshotFormat 进快照名：改了 initdb 参数或 clusterConf 时加一，旧快照自然作废。
	snapshotFormat = 2
)

// clusterConf 追加到私有集群的 postgresql.conf。数据随进程丢弃，关掉一切持久化保证；
// 只监听回环地址的 TCP，不建 Unix socket（避免路径长度和权限问题，Windows 也一样）。
const clusterConf = `
listen_addresses = '127.0.0.1'
unix_socket_directories = ''
fsync = off
synchronous_commit = off
full_page_writes = off
max_connections = 200
max_locks_per_transaction = 512
min_wal_size = 2MB
wal_recycle = off
`

// privateCluster 是本测试进程独占的一个 PostgreSQL 实例：从迁移快照拷贝数据目录、随机端口
// 启动，进程结束时停止并删除。被强杀留下的实例由下一次启动时的 sweepOrphans 清理。
type privateCluster struct {
	dist    string
	runDir  string
	dataDir string
}

func startPrivateCluster() (*privateCluster, string, error) {
	home, err := homeDir()
	if err != nil {
		return nil, "", err
	}
	dist, err := postgresDist(home)
	if err != nil {
		return nil, "", err
	}
	root := runRoot(home)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, "", err
	}
	sweepOrphans(dist, root)
	snapshot, err := ensureSnapshot(home, dist, root)
	if err != nil {
		return nil, "", err
	}
	c, err := newRunDir(dist, root)
	if err != nil {
		return nil, "", err
	}
	if err := copyTree(filepath.Join(snapshot, "data"), c.dataDir); err != nil {
		c.discard()
		return nil, "", fmt.Errorf("copy snapshot: %w", err)
	}
	if err := handOver(c.runDir); err != nil {
		c.discard()
		return nil, "", err
	}
	port, err := c.start()
	if err != nil {
		c.discard()
		return nil, "", err
	}
	return c, clusterDSN(port, clusterDatabase), nil
}

// newRunDir 建一个属于本进程的运行目录，owner.pid 让 sweepOrphans 判断它是否还有主人。
func newRunDir(dist, root string) (*privateCluster, error) {
	runDir := filepath.Join(root, strconv.Itoa(os.Getpid())+"-"+randomHex(4))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(runDir, "owner.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		_ = os.RemoveAll(runDir)
		return nil, err
	}
	return &privateCluster{dist: dist, runDir: runDir, dataDir: filepath.Join(runDir, "data")}, nil
}

func clusterDSN(port int, database string) string {
	return fmt.Sprintf("postgres://%s@127.0.0.1:%d/%s?sslmode=disable", clusterUser, port, database)
}

func (c *privateCluster) tool(name string) string {
	return filepath.Join(c.dist, "bin", exeName(name))
}

// run 执行一个 PostgreSQL 工具，失败时带上输出和平台相关的提示。
func (c *privateCluster) run(name string, args ...string) error {
	cmd, err := pgCommand(c.tool(name), args...)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf("%s %s: %v: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	if hint := explainExit(err); hint != "" {
		msg += "\n" + hint
	}
	return errors.New(msg)
}

// start 在一个空闲端口上启动集群并等待就绪；端口在探测和绑定之间被抢走时换一个重试。
func (c *privateCluster) start() (int, error) {
	logFile := filepath.Join(c.runDir, "postgres.log")
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		port, err := freePort()
		if err != nil {
			return 0, err
		}
		// postgresql.conf 里后出现的设置生效，重试时追加即可。
		if err := appendFile(filepath.Join(c.dataDir, "postgresql.conf"), fmt.Sprintf("\nport = %d\n", port)); err != nil {
			return 0, err
		}
		lastErr = c.run("pg_ctl", "-D", c.dataDir, "-l", logFile, "-w", "-t", "120", "start")
		if lastErr == nil {
			return port, nil
		}
		log, _ := os.ReadFile(logFile)
		if !bytes.Contains(log, []byte("could not bind")) && !bytes.Contains(log, []byte("Address already in use")) {
			break
		}
	}
	log, _ := os.ReadFile(logFile)
	return 0, fmt.Errorf("start PostgreSQL: %w\n%s", lastErr, tail(log, 2000))
}

// stop 立即停止集群并删除运行目录：数据本来就要丢弃，不需要干净关闭。
func (c *privateCluster) stop() {
	_ = c.run("pg_ctl", "-D", c.dataDir, "-m", "immediate", "-w", "stop")
	c.discard()
}

func (c *privateCluster) discard() {
	// Windows 上 postgres 子进程退出后文件句柄会晚一点释放。
	for attempt := 0; attempt < 20; attempt++ {
		if err := os.RemoveAll(c.runDir); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ensureSnapshot 返回已迁移到 head 的集群数据目录快照（含模板库）。快照按 PostgreSQL 版本和
// 迁移内容哈希命名，迁移不变就一直复用；只有第一次（或迁移变了）才从 initdb 开始建。
func ensureSnapshot(home, dist, root string) (string, error) {
	hash, err := migrationsHash()
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("pg%s-f%d-%s", PostgresVersion, snapshotFormat, hash)
	final := filepath.Join(home, "snapshots", key)
	err = buildOnce(final, func(tmp string) error {
		fmt.Fprintf(os.Stderr, "pgtest: building PostgreSQL snapshot %s (once per migration change)\n", key)
		return buildSnapshot(dist, root, tmp)
	})
	if err != nil {
		return "", fmt.Errorf("build snapshot %s: %w", key, err)
	}
	now := time.Now()
	_ = os.Chtimes(final, now, now)
	pruneSnapshots(filepath.Dir(final), key)
	return final, nil
}

// buildSnapshot 在运行目录里 initdb、迁移出模板库、干净停机，再把数据目录拷进 dst。
// 在运行目录而不是 home 里建：Linux 上是内存盘，root 运行时也只需把这一处交给 postgres 用户。
func buildSnapshot(dist, root, dst string) error {
	c, err := newRunDir(dist, root)
	if err != nil {
		return err
	}
	defer c.discard()
	if err := handOver(c.runDir); err != nil {
		return err
	}
	if err := c.initdb(); err != nil {
		return err
	}
	if err := appendFile(filepath.Join(c.dataDir, "postgresql.conf"), clusterConf); err != nil {
		return err
	}
	port, err := c.start()
	if err != nil {
		return err
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = c.run("pg_ctl", "-D", c.dataDir, "-m", "immediate", "-w", "stop")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, clusterDSN(port, "postgres"))
	if err != nil {
		return fmt.Errorf("connect new cluster: %w", err)
	}
	_, err = conn.Exec(ctx, `CREATE DATABASE `+ident(clusterDatabase))
	conn.Close(context.Background())
	if err != nil {
		return fmt.Errorf("create %s: %w", clusterDatabase, err)
	}
	if _, err := ensureTemplate(clusterDSN(port, clusterDatabase)); err != nil {
		return err
	}
	// 干净停机：快照要能直接启动，不能带着需要崩溃恢复的 WAL。
	if err := c.run("pg_ctl", "-D", c.dataDir, "-m", "fast", "-w", "stop"); err != nil {
		return err
	}
	stopped = true
	return copyTree(c.dataDir, filepath.Join(dst, "data"))
}

func (c *privateCluster) initdb() error {
	var errs []error
	for _, locale := range localeCandidates() {
		err := c.run("initdb", "-D", c.dataDir, "-U", clusterUser, "-A", "trust", "-E", "UTF8",
			"--locale="+locale, "--no-sync", "--no-instructions",
			// 1MB 的 WAL 段（默认 16MB）：快照里的 pg_wal 从 32MB 降到几 MB，拷贝也更快。
			"--wal-segsize=1")
		if err == nil {
			if locale == "C" {
				fmt.Fprintln(os.Stderr, "pgtest: no UTF-8 locale available, using C: pg_trgm tests on non-ASCII text may fail")
			}
			return nil
		}
		errs = append(errs, err)
		_ = os.RemoveAll(c.dataDir)
	}
	return fmt.Errorf("initdb: %w", errors.Join(errs...))
}

// sweepOrphans 停掉并删除主人进程已不在的运行目录：测试超时或被 Ctrl-C 杀掉时 Run 来不及清理。
func sweepOrphans(dist, root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "owner.pid"))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || processAlive(pid) {
			continue
		}
		c := &privateCluster{dist: dist, runDir: dir, dataDir: filepath.Join(dir, "data")}
		if _, err := os.Stat(filepath.Join(c.dataDir, "postmaster.pid")); err == nil {
			c.stop()
		} else {
			c.discard()
		}
	}
}

// pruneSnapshots 删除迁移已变化、且 snapshotTTL 内没用过的旧快照，以及被杀构建留下的临时目录。
func pruneSnapshots(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == keep || strings.HasSuffix(name, ".lock") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		ttl := snapshotTTL
		if strings.Contains(name, ".tmp-") {
			ttl = staleLock
		}
		if time.Since(info.ModTime()) > ttl {
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
}

// buildOnce 在 final 不存在时用 build 生成它。go test 并行跑多个包时多个进程会同时需要它：
// 只有建出 final+".lock" 目录的进程构建，其余等待。build 写同级临时目录，完成后原子 rename，
// 中途失败或进程被杀都不会留下半成品；锁超过 staleLock 没刷新视为被杀进程的残留。
func buildOnce(final string, build func(tmp string) error) error {
	if exists(final) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	lock := final + ".lock"
	deadline := time.Now().Add(30 * time.Minute)
	for {
		if exists(final) {
			return nil
		}
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > staleLock {
			_ = os.RemoveAll(lock)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for another process to build %s (remove %s if it was killed)", final, lock)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer os.RemoveAll(lock)
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(staleLock / 4)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				_ = os.Chtimes(lock, now, now)
			}
		}
	}()
	if exists(final) {
		return nil
	}
	tmp := final + ".tmp-" + randomHex(4)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := build(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

// homeDir 返回 EnvHome，或从当前目录（go test 时是包目录）向上找到的模块根下的 .pgtest。
func homeDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvHome)); dir != "" {
		return filepath.Abs(dir)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; {
		if exists(filepath.Join(dir, "go.mod")) {
			return filepath.Join(dir, ".pgtest"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s: set %s", wd, EnvHome)
		}
		dir = parent
	}
}

// runRoot 是运行中集群的父目录：Linux 有 /dev/shm 就放内存盘（按 home 区分不同检出）；
// 其余平台放 home/run，不落到系统盘的临时目录。
func runRoot(home string) string {
	if runtime.GOOS == "linux" {
		if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
			sum := sha256.Sum256([]byte(home))
			return filepath.Join("/dev/shm", "telesrv-pgtest-"+hex.EncodeToString(sum[:4]))
		}
	}
	return filepath.Join(home, "run")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// copyTree 复制目录树并保留权限位（PostgreSQL 要求数据目录是 0700/0750）。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return nil
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if err := out.Close(); copyErr == nil {
		copyErr = err
	}
	return copyErr
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(text)
	if err := f.Close(); writeErr == nil {
		writeErr = err
	}
	return writeErr
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
