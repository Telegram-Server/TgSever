//go:build unix

package updatecdn

import (
	"io/fs"
	"os"
	"syscall"
)

// copyOwner 让替换后的清单沿用原清单的属主与属组：以 root 在宿主机上发布时，不能让
// 以服务用户运行的进程读不到新清单。只有特权用户能改属主，失败时保持发布者为属主
// （此时发布者与服务通常是同一用户）。
func copyOwner(path string, previous fs.FileInfo) {
	if st, ok := previous.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
}
