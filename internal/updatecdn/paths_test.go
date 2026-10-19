package updatecdn

import (
	"path/filepath"
	"testing"
)

// 文件目录未显式配置时取清单所在目录下的 files；telesrv 与 telesrv-update 共用这条规则，
// 两边对空白的处理也必须一致。
func TestDefaultFilesDir(t *testing.T) {
	updates := filepath.Join("var", "lib", "telesrv-updates")
	for _, tc := range []struct {
		name, manifest, filesDir, want string
	}{
		{name: "next to manifest", manifest: filepath.Join(updates, "manifest.json"), want: filepath.Join(updates, "files")},
		{name: "bare manifest name", manifest: "manifest.json", want: "files"},
		{name: "manifest with spaces", manifest: "  " + filepath.Join(updates, "manifest.json") + "\t", want: filepath.Join(updates, "files")},
		{name: "blank files dir", manifest: filepath.Join(updates, "manifest.json"), filesDir: "   ", want: filepath.Join(updates, "files")},
		{name: "explicit files dir", manifest: filepath.Join(updates, "manifest.json"), filesDir: " /srv/packages ", want: "/srv/packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultFilesDir(tc.manifest, tc.filesDir); got != tc.want {
				t.Fatalf("DefaultFilesDir(%q, %q) = %q, want %q", tc.manifest, tc.filesDir, got, tc.want)
			}
		})
	}
}
