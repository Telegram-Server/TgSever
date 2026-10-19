package main

import (
	"flag"
	"io"
	"path/filepath"
	"testing"
)

// 服务模式与 publish 共用的路径参数：环境变量给默认值，参数覆盖环境变量；文件目录未显式
// 配置时取清单所在目录下的 files，与 telesrv 进程内更新服务的默认值一致——Docker 里只设置
// TELESRV_UPDATE_MANIFEST 时，publish 写入的目录必须就是服务读取的目录。
func TestCatalogPathsResolveFromEnvAndFlags(t *testing.T) {
	updates := filepath.Join("/var", "lib", "telesrv-updates")
	manifest := filepath.Join(updates, "manifest.json")
	key := filepath.Join("/etc", "telesrv-updates", "update-root.pub")
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want catalogPaths
	}{
		{name: "built-in defaults", want: catalogPaths{
			Manifest: filepath.Join("data", "updates", "manifest.json"),
			FilesDir: filepath.Join("data", "updates", "files"),
		}},
		{name: "manifest and key from env", env: map[string]string{"TELESRV_UPDATE_MANIFEST": manifest, "TELESRV_UPDATE_ROOT_PUBLIC_KEY": key},
			want: catalogPaths{Manifest: manifest, FilesDir: filepath.Join(updates, "files"), RootPublicKey: key}},
		{name: "manifest and key from flags", args: []string{"-manifest", manifest, "-root-public-key", key},
			want: catalogPaths{Manifest: manifest, FilesDir: filepath.Join(updates, "files"), RootPublicKey: key}},
		{name: "flags override env", env: map[string]string{"TELESRV_UPDATE_MANIFEST": "/elsewhere/manifest.json", "TELESRV_UPDATE_ROOT_PUBLIC_KEY": "/elsewhere/root.pub"},
			args: []string{"-manifest", manifest, "-root-public-key", key},
			want: catalogPaths{Manifest: manifest, FilesDir: filepath.Join(updates, "files"), RootPublicKey: key}},
		{name: "explicit env files dir", env: map[string]string{"TELESRV_UPDATE_MANIFEST": manifest, "TELESRV_UPDATE_FILES_DIR": "/srv/packages"},
			want: catalogPaths{Manifest: manifest, FilesDir: "/srv/packages"}},
		{name: "explicit flag files dir", args: []string{"-manifest", manifest, "-files", "/srv/packages"},
			want: catalogPaths{Manifest: manifest, FilesDir: "/srv/packages"}},
		{name: "blank files dir falls back like telesrv", env: map[string]string{"TELESRV_UPDATE_FILES_DIR": "  "}, args: []string{"-manifest", " " + manifest + " "},
			want: catalogPaths{Manifest: manifest, FilesDir: filepath.Join(updates, "files")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TELESRV_UPDATE_MANIFEST", "")
			t.Setenv("TELESRV_UPDATE_FILES_DIR", "")
			t.Setenv("TELESRV_UPDATE_ROOT_PUBLIC_KEY", "")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			flags := registerCatalogFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := resolveCatalogPaths(flags); got != tc.want {
				t.Fatalf("catalog paths = %+v, want %+v", got, tc.want)
			}
		})
	}
}
