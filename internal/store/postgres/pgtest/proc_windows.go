//go:build windows

package pgtest

import (
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"syscall"
)

// localeCandidates 依次尝试的 initdb locale。Windows 没有 C.UTF-8；BCP 47 名称配合
// -E UTF8 即可，LC_CTYPE 仍按 Unicode 处理大小写。
func localeCandidates() []string {
	return []string{"en-US", "C"}
}

// pgCommand 构造运行 PostgreSQL 工具的命令。管理员身份运行时 initdb、pg_ctl 会自己用受限
// 令牌降权启动 postgres，不需要另外切换用户。
func pgCommand(name string, args ...string) (*exec.Cmd, error) {
	return exec.Command(name, args...), nil
}

func handOver(string) error { return nil }

// prepareRunRoot 给当前用户在运行根目录上加一条可继承的完全控制权限。以管理员身份运行时，
// 本进程拷贝出的数据文件归 Administrators 组所有；pg_ctl 用受限令牌启动 postgres，正好去掉了
// 这个组，postgres 就打不开自己的 global/pg_control。initdb 自己建的文件不受影响。用户 SID
// 在受限令牌里仍然有效，之后在这里创建的文件都继承这条 ACE。
func prepareRunRoot(dir string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	out, err := exec.Command("icacls", dir, "/grant", "*"+u.Uid+":(OI)(CI)F", "/Q").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls %s: %v: %s", dir, err, out)
	}
	return nil
}

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	statusDLLNotFound              = 0xC0000135
)

func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// explainExit 把常见的 Windows 启动失败翻译成可操作的提示。
func explainExit(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && uint32(exitErr.ExitCode()) == statusDLLNotFound {
		return "PostgreSQL needs the Microsoft Visual C++ 2015-2022 x64 runtime (VCRUNTIME140.dll): " +
			"install https://aka.ms/vs/17/release/vc_redist.x64.exe"
	}
	return ""
}
