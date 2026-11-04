//go:build windows

package wallet

import "io/fs"

// privateModeEnforced 在 Windows 上为 false：Go 报告的权限位由只读属性推出，与真实
// ACL 无关，0700 / 0600 的检查永远不会通过。Windows 上钱包目录与主密钥的访问控制靠
// 所在目录的 ACL；生产部署在 Linux 容器里，仍按 POSIX 权限检查。
const privateModeEnforced = false

func isPrivateMode(fs.FileMode) bool { return true }

// syncDir 在 Windows 上是空操作：目录不能以可 fsync 的方式打开（会得到 Access is
// denied），NTFS 的元数据日志负责目录项的持久性。
func syncDir(string) error { return nil }
