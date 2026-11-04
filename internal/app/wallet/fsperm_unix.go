//go:build !windows

package wallet

import (
	"io/fs"
	"os"
)

// privateModeEnforced 报告本平台的权限位能否表达"只有属主可访问"。POSIX 上能，
// 钱包目录必须是 0700、主密钥文件必须是 0600。
const privateModeEnforced = true

// isPrivateMode 报告 mode 是否没有给同组或其他用户任何权限。
func isPrivateMode(mode fs.FileMode) bool {
	return mode.Perm()&0077 == 0
}

// syncDir 把目录项（新建、改名）落盘，保证崩溃后文件不会凭空消失。
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
