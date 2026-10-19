package main

import (
	"flag"
	"io"
	"path/filepath"
	"testing"
)

// 文件目录未显式配置时取清单所在目录下的 files，与 telesrv 进程内更新服务的默认值一致：
// Docker 里只设置 TELESRV_UPDATE_MANIFEST 时，publish 写入的目录必须就是服务读取的目录。
func TestCatalogFilesDirDefaultsNextToManifest(t *testing.T) {
	updates := filepath.Join("/var", "lib", "telesrv-updates")
	for _, tc := range []struct {
		name      string
		env       map[string]string
		args      []string
		wantFiles string
	}{
		{name: "built-in defaults", wantFiles: filepath.Join("data", "updates", "files")},
		{name: "manifest from env", env: map[string]string{"TELESRV_UPDATE_MANIFEST": filepath.Join(updates, "manifest.json")}, wantFiles: filepath.Join(updates, "files")},
		{name: "manifest from flag", args: []string{"-manifest", filepath.Join(updates, "manifest.json")}, wantFiles: filepath.Join(updates, "files")},
		{name: "explicit env files dir", env: map[string]string{"TELESRV_UPDATE_MANIFEST": filepath.Join(updates, "manifest.json"), "TELESRV_UPDATE_FILES_DIR": "/srv/packages"}, wantFiles: "/srv/packages"},
		{name: "explicit flag files dir", args: []string{"-manifest", filepath.Join(updates, "manifest.json"), "-files", "/srv/packages"}, wantFiles: "/srv/packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TELESRV_UPDATE_MANIFEST", "")
			t.Setenv("TELESRV_UPDATE_FILES_DIR", "")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			paths := catalogFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := paths().FilesDir; got != tc.wantFiles {
				t.Fatalf("files dir = %q, want %q", got, tc.wantFiles)
			}
		})
	}
}
