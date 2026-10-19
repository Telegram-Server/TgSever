package updatecdn

import (
	"path/filepath"
	"strings"
)

// DefaultFilesDir returns the desktop package directory for a manifest: the
// explicitly configured filesDir, or "files" next to the manifest when it is
// empty. Both arguments are trimmed of surrounding whitespace first.
//
// telesrv（TELESRV_UPDATE_FILES_DIR）与 telesrv-update（-files）共用这条规则：只设置清单
// 路径时，publish 写入的目录必须就是服务读取的目录。
func DefaultFilesDir(manifest, filesDir string) string {
	if filesDir = strings.TrimSpace(filesDir); filesDir != "" {
		return filesDir
	}
	return filepath.Join(filepath.Dir(strings.TrimSpace(manifest)), "files")
}
