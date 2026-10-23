//go:build !windows

package pgtest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// EnvRunAs 指定以 root 运行测试时 PostgreSQL 使用的系统用户（PostgreSQL 拒绝以 root 运行）。
// 未设置时依次尝试 postgres、nobody。
const EnvRunAs = "TELESRV_TEST_POSTGRES_RUN_AS"

// localeCandidates 依次尝试的 initdb locale：需要 UTF-8 的 LC_CTYPE，否则 pg_trgm 对
// 西里尔字母等非 ASCII 文本的大小写和分词会出错。
func localeCandidates() []string {
	return []string{"C.UTF-8", "en_US.UTF-8", "C"}
}

// pgCommand 构造运行 PostgreSQL 工具的命令；root 下切换到 runAsIDs 的用户。
func pgCommand(name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.Command(name, args...)
	if os.Geteuid() != 0 {
		return cmd, nil
	}
	uid, gid, err := runAsIDs()
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
	return cmd, nil
}

// handOver 把 path 整棵树交给运行 PostgreSQL 的用户；非 root 时什么也不做。
func handOver(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	uid, gid, err := runAsIDs()
	if err != nil {
		return err
	}
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(uid), int(gid))
	})
}

func runAsIDs() (uint32, uint32, error) {
	names := []string{"postgres", "nobody"}
	if name := strings.TrimSpace(os.Getenv(EnvRunAs)); name != "" {
		names = []string{name}
	}
	for _, name := range names {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return 0, 0, fmt.Errorf("user %s uid %q: %w", name, u.Uid, err)
		}
		gid, err := strconv.ParseUint(u.Gid, 10, 32)
		if err != nil {
			return 0, 0, fmt.Errorf("user %s gid %q: %w", name, u.Gid, err)
		}
		return uint32(uid), uint32(gid), nil
	}
	return 0, 0, fmt.Errorf("PostgreSQL refuses to run as root and none of %v exists: set %s to a non-root user", names, EnvRunAs)
}

// prepareRunRoot 在 Unix 上无事可做：root 运行时由 handOver 逐目录交给 postgres 用户。
func prepareRunRoot(string) error { return nil }

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// explainExit 解释 root 运行时最常见的失败：切换后的用户进不了 home 所在目录。
func explainExit(err error) string {
	if os.Geteuid() == 0 && errors.Is(err, fs.ErrPermission) {
		return "PostgreSQL runs as a non-root user that cannot reach this path: put " + EnvHome +
			" in a directory every user can traverse, or set " + EnvRunAs
	}
	return ""
}
