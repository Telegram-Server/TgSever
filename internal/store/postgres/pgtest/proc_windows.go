//go:build windows

package pgtest

import (
	"errors"
	"os/exec"
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
