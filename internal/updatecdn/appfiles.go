package updatecdn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Android 安装包的来源：telesrv 把 APK 注册成 Telegram 文档时，从这里列出要注册的包、
// 读出包的字节。进程内（*Store）直接读文件目录；独立部署（*Client）经 telesrv-update 的
// /v1/app-files 与 /files/ 读取。两种来源返回的字节都按 ResolvedFile 的 SHA256 与 Size
// 校验，不一致时读取以错误结束。

// AppFileSource lists and opens the installer packages of the update catalog.
type AppFileSource interface {
	// AppFiles 列出启用的 Android 条目引用的安装包（每个文件一次）。
	AppFiles(ctx context.Context) ([]ResolvedFile, error)
	// OpenAppFile 打开一个安装包。返回的 reader 读到末尾时核对大小与 SHA256，不一致
	// 返回 ErrAppFileMismatch。
	OpenAppFile(ctx context.Context, file ResolvedFile) (io.ReadCloser, error)
}

var (
	_ AppFileSource = (*Store)(nil)
	_ AppFileSource = (*Client)(nil)
)

var (
	// ErrAppFileNotFound 表示目录里（已）没有这个安装包：清单换了版本、条目被停用。
	ErrAppFileNotFound = errors.New("installer package not found in the update catalog")
	// ErrAppFileMismatch 表示读到的字节与清单记录的大小或 SHA256 不一致。
	ErrAppFileMismatch = errors.New("installer package does not match its size or SHA256")
)

// AppFiles lists the installer packages of the current snapshot.
func (s *Store) AppFiles(context.Context) ([]ResolvedFile, error) {
	catalog, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	return catalog.AppFiles(), nil
}

// OpenAppFile opens an installer package of the current snapshot from the files
// directory. It refuses a file the catalog does not list with these exact
// size and SHA256, and one that changed on disk since the catalog verified it.
func (s *Store) OpenAppFile(_ context.Context, file ResolvedFile) (io.ReadCloser, error) {
	catalog, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	record, ok := catalog.file(file.Name)
	if !ok || record.apk == nil || !strings.EqualFold(record.sha256, file.SHA256) || record.size != file.Size {
		return nil, fmt.Errorf("%w: %s", ErrAppFileNotFound, file.Name)
	}
	f, err := os.Open(record.path)
	if err != nil {
		return nil, fmt.Errorf("open installer package %q: %w", file.Name, err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != record.size || !info.ModTime().Equal(record.modTime) {
		_ = f.Close()
		return nil, fmt.Errorf("installer package %q changed on disk; reload the manifest", file.Name)
	}
	return newVerifyingReader(f, file), nil
}

// appFilesResponse 是 /v1/app-files 的响应体。
type appFilesResponse struct {
	Files []ResolvedFile `json:"files"`
}

// AppFiles lists the installer packages of a separate telesrv-update.
func (c *Client) AppFiles(ctx context.Context) ([]ResolvedFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/app-files", nil)
	if err != nil {
		return nil, fmt.Errorf("build app files request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list installer packages: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("list installer packages: HTTP %d", resp.StatusCode)
	}
	var out appFilesResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode installer packages: %w", err)
	}
	for _, file := range out.Files {
		if !file.Valid() {
			return nil, fmt.Errorf("list installer packages: invalid entry %q", file.Name)
		}
	}
	return out.Files, nil
}

// OpenAppFile streams an installer package from a separate telesrv-update's
// /files/. The body is read with the context's deadline only (the client's
// short request timeout is for metadata), and verified as it is read.
func (c *Client) OpenAppFile(ctx context.Context, file ResolvedFile) (io.ReadCloser, error) {
	if !file.Valid() {
		return nil, fmt.Errorf("%w: %s", ErrAppFileNotFound, file.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/files/"+url.PathEscape(file.Name), nil)
	if err != nil {
		return nil, fmt.Errorf("build installer package request: %w", err)
	}
	// 下载可能有上百 MB，不能用元数据请求的短超时；时限由调用方的 context 决定。
	resp, err := (&http.Client{Transport: c.http.Transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download installer package %q: %w", file.Name, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrAppFileNotFound, file.Name)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download installer package %q: HTTP %d", file.Name, resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != file.Size {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s is %d bytes, want %d", ErrAppFileMismatch, file.Name, resp.ContentLength, file.Size)
	}
	return newVerifyingReader(resp.Body, file), nil
}

// verifyingReader 读到末尾时核对大小与 SHA256；超出大小立即报错，不会被一个超长的
// 响应拖住。
type verifyingReader struct {
	rc   io.ReadCloser
	file ResolvedFile
	hash hash.Hash
	read int64
}

func newVerifyingReader(rc io.ReadCloser, file ResolvedFile) *verifyingReader {
	return &verifyingReader{rc: rc, file: file, hash: sha256.New()}
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.rc.Read(p)
	if n > 0 {
		v.read += int64(n)
		if v.read > v.file.Size {
			return 0, fmt.Errorf("%w: %s is longer than %d bytes", ErrAppFileMismatch, v.file.Name, v.file.Size)
		}
		v.hash.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		if v.read != v.file.Size || !strings.EqualFold(hex.EncodeToString(v.hash.Sum(nil)), v.file.SHA256) {
			return n, fmt.Errorf("%w: %s", ErrAppFileMismatch, v.file.Name)
		}
	}
	return n, err
}

func (v *verifyingReader) Close() error { return v.rc.Close() }
