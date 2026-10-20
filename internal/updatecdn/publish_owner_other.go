//go:build !unix

package updatecdn

import "io/fs"

// copyOwner 在非 Unix 平台上不做任何事：文件属主由目录 ACL 继承。
func copyOwner(string, fs.FileInfo) {}
