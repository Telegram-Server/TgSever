package pgtest

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// PostgresVersion 是私有集群使用的 PostgreSQL 版本，二进制来自 zonky 的
// embedded-postgres-binaries（Maven Central 上按平台发布的独立包，自带依赖库）。
const PostgresVersion = "17.11.0"

// EnvBin 指向一个已解压的 PostgreSQL 目录（含 bin/initdb、bin/pg_ctl），设置后不再下载。
const EnvBin = "TELESRV_TEST_POSTGRES_BIN"

// EnvMirror 是额外的 Maven 仓库根地址，排在内置镜像之前尝试。
const EnvMirror = "TELESRV_TEST_POSTGRES_MIRROR"

type distArtifact struct {
	name   string // Maven artifactId 的平台后缀
	sha256 string // 整个 jar 的 SHA-256，下载后逐字节校验
}

// distArtifacts 固定了每个平台 jar 的哈希：镜像被篡改或下错版本都会被拒绝。
// 升级 PostgresVersion 时一并更新。
var distArtifacts = map[string]distArtifact{
	"linux/amd64":   {"linux-amd64", "0dd7b72b6f335b8ecfb355fa24c5781e8a93edd09880bb77eb52ebbf29b3e96d"},
	"linux/arm64":   {"linux-arm64v8", "8b042e0ea418b1927d95399207950da0734d27fe01e29503ade3bdc464ad4c2b"},
	"darwin/amd64":  {"darwin-amd64", "d464ff178e9860ba204662ac23fa547504b7fd392392ff2fb92e3fd73b5bdb64"},
	"darwin/arm64":  {"darwin-arm64v8", "a1c2786acb0c398f9b2d76806fc52f5dc8b222cbc8e9383a9b9702084daaf3a5"},
	"windows/amd64": {"windows-amd64", "98040fae18dd9633ff95932125b0cecf0a45a1a9312e216e2a33ad03a31d4251"},
}

// mavenMirrors 依次尝试：Maven Central 主站会对同一出口 IP 限流（429），国内访问也可能很慢。
var mavenMirrors = []string{
	"https://repo1.maven.org/maven2",
	"https://maven-central.storage-download.googleapis.com/maven2",
	"https://maven.aliyun.com/repository/public",
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func hasPostgresBinaries(dir string) bool {
	for _, name := range []string{"initdb", "pg_ctl", "postgres"} {
		if _, err := os.Stat(filepath.Join(dir, "bin", exeName(name))); err != nil {
			return false
		}
	}
	return true
}

// postgresDist 返回 PostgreSQL 安装目录：EnvBin，或 home/dist 下按版本缓存的解压目录，
// 缺失时下载。
func postgresDist(home string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvBin)); dir != "" {
		if !hasPostgresBinaries(dir) {
			return "", fmt.Errorf("%s=%s: bin/initdb, bin/pg_ctl or bin/postgres is missing", EnvBin, dir)
		}
		return dir, nil
	}
	key := runtime.GOOS + "/" + runtime.GOARCH
	artifact, ok := distArtifacts[key]
	if !ok {
		return "", fmt.Errorf("no PostgreSQL %s binaries for %s: set %s to a local PostgreSQL 17 directory, or %s to an existing server",
			PostgresVersion, key, EnvBin, EnvDSN)
	}
	dir := filepath.Join(home, "dist", "postgres-"+PostgresVersion+"-"+artifact.name)
	err := buildOnce(dir, func(tmp string) error {
		return downloadDist(artifact, tmp)
	})
	if err != nil {
		return "", err
	}
	if !hasPostgresBinaries(dir) {
		return "", fmt.Errorf("%s is incomplete: delete it and run the tests again", dir)
	}
	return dir, nil
}

func downloadDist(artifact distArtifact, dst string) error {
	mirrors := mavenMirrors
	if extra := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvMirror)), "/"); extra != "" {
		mirrors = append([]string{extra}, mirrors...)
	}
	jar := "embedded-postgres-binaries-" + artifact.name + "-" + PostgresVersion + ".jar"
	path := "io/zonky/test/postgres/embedded-postgres-binaries-" + artifact.name + "/" + PostgresVersion + "/" + jar
	jarFile := filepath.Join(dst, jar)
	var errs []error
	for _, mirror := range mirrors {
		url := mirror + "/" + path
		fmt.Fprintf(os.Stderr, "pgtest: downloading PostgreSQL %s from %s\n", PostgresVersion, url)
		if err := fetchVerified(url, jarFile, artifact.sha256); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mirror, err))
			continue
		}
		defer os.Remove(jarFile)
		return extractJar(jarFile, dst)
	}
	return fmt.Errorf("download PostgreSQL %s (or set %s to a local copy): %w", PostgresVersion, EnvBin, errors.Join(errs...))
}

func fetchVerified(url, path, wantSHA256 string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	if err := f.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		return copyErr
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA256 {
		return fmt.Errorf("sha256 %s, want %s", got, wantSHA256)
	}
	return nil
}

// extractJar 解出 jar 里唯一的 postgres-*.txz 到 dst。
func extractJar(jarFile, dst string) error {
	zr, err := zip.OpenReader(jarFile)
	if err != nil {
		return fmt.Errorf("open %s: %w", jarFile, err)
	}
	defer zr.Close()
	for _, entry := range zr.File {
		if !strings.HasSuffix(entry.Name, ".txz") {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		xr, err := xz.NewReader(rc)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name, err)
		}
		return extractTar(tar.NewReader(xr), dst)
	}
	return fmt.Errorf("%s has no .txz entry", jarFile)
}

func extractTar(tr *tar.Reader, dst string) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %q escapes the target directory", hdr.Name)
		}
		target := filepath.Join(dst, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			if err := f.Close(); copyErr == nil {
				copyErr = err
			}
			if copyErr != nil {
				return copyErr
			}
		case tar.TypeSymlink:
			// Linux/macOS 包里的 libxxx.so → libxxx.so.N；只接受指向包内的相对链接。
			link := filepath.FromSlash(hdr.Linkname)
			if filepath.IsAbs(link) || !strings.HasPrefix(filepath.Clean(filepath.Join(filepath.Dir(target), link)), filepath.Clean(dst)+string(filepath.Separator)) {
				return fmt.Errorf("archive symlink %q -> %q escapes the target directory", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		}
	}
}
