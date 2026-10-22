package updatecdn

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHandlerServesCurrentResolveAndRange(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageData := testDesktopPackage(t, "win64", "stable", 7007000)
	packageName := "td-update-win-x64-7007000"
	if err := os.WriteFile(filepath.Join(filesDir, packageName), packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(packageData)
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {Build: 7007000, File: packageName, SHA256: hex.EncodeToString(hash[:])}},
		},
		Apps: map[string]map[string]ApplicationRelease{
			"ios": {"stable": {ID: 8, Version: "12.9.1", URL: "https://apps.apple.com/app/id1", Notes: map[string]string{"en": "Update available"}}},
		},
	})
	store, err := NewStore(manifestPath, filesDir, testCatalogOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/current4")
	if err != nil {
		t.Fatal(err)
	}
	currentBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(currentBody), `"released":7007000`) {
		t.Fatalf("current4 = %d %s", resp.StatusCode, currentBody)
	}

	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := client.Resolve(t.Context(), ResolveRequest{Platform: "ios", Version: "12.9.0", LangCode: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.ID != 8 || resolved.Version != "12.9.1" {
		t.Fatalf("resolved = %#v", resolved)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/files/"+packageName, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=2-5")
	rangeResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rangeBody, _ := io.ReadAll(rangeResp.Body)
	rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent || string(rangeBody) != string(packageData[2:6]) {
		t.Fatalf("range = %d %q", rangeResp.StatusCode, rangeBody)
	}
}

// newWin64Handler 发布一个 win64 stable 包并返回加载它的 Handler 与包内容。
func newWin64Handler(t *testing.T, storeOpts ...StoreOption) (*Handler, []byte, string) {
	t.Helper()
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	if err := os.Mkdir(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageData := testDesktopPackage(t, "win64", "stable", 7007000)
	packageName := "td-update-win-x64-7007000"
	if err := os.WriteFile(filepath.Join(filesDir, packageName), packageData, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(packageData)
	manifestPath := writeTestManifest(t, dir, Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Desktop: map[string]map[string]DesktopRelease{
			"win64": {"stable": {Build: 7007000, File: packageName, SHA256: hex.EncodeToString(hash[:])}},
		},
	})
	store, err := NewStore(manifestPath, filesDir, testCatalogOptions(t), storeOpts...)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	return handler, packageData, manifestPath
}

func getBody(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// /current 与 /current1 … /current10 返回同一份桌面更新表；范围之外的 /currentN 不存在。
func TestHandlerServesSameDesktopMapOnEveryCurrentEndpoint(t *testing.T) {
	handler, _, _ := newWin64Handler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	status, want := getBody(t, server.URL+"/current4")
	if status != http.StatusOK || !strings.Contains(string(want), `"released":7007000`) {
		t.Fatalf("current4 = %d %s", status, want)
	}
	endpoints := []string{"/current"}
	for n := 1; n <= 10; n++ {
		endpoints = append(endpoints, "/current"+strconv.Itoa(n))
	}
	for _, endpoint := range endpoints {
		resp, err := http.Get(server.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body, want) {
			t.Fatalf("%s = %d %s, want the current4 body %s", endpoint, resp.StatusCode, body, want)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s Cache-Control = %q, want no-cache", endpoint, got)
		}
	}
	for _, endpoint := range []string{"/current0", "/current11"} {
		if status, body := getBody(t, server.URL+endpoint); status != http.StatusNotFound {
			t.Fatalf("%s = %d %s, want 404", endpoint, status, body)
		}
	}
}

// 主进程把 Handler 挂在公开 Web 的 /_updates/ 下（剥离前缀后转发）。客户端以
// autoupdate_url_prefix = <公开地址>/_updates 请求 current4，再把相对 link 拼到
// 同一前缀后下载。
func TestHandlerMountedUnderPrefixServesLinkedPackage(t *testing.T) {
	handler, packageData, _ := newWin64Handler(t)
	mux := http.NewServeMux()
	mux.Handle("/_updates/", http.StripPrefix("/_updates", handler))
	server := httptest.NewServer(mux)
	defer server.Close()
	prefix := server.URL + "/_updates"

	status, body := getBody(t, prefix+"/current4")
	if status != http.StatusOK {
		t.Fatalf("current4 = %d %s", status, body)
	}
	var current map[string]map[string]struct {
		Released int64  `json:"released"`
		Link     string `json:"link"`
	}
	if err := json.Unmarshal(body, &current); err != nil {
		t.Fatalf("decode current4 %s: %v", body, err)
	}
	link := current["win64"]["stable"].Link
	if link == "" {
		t.Fatalf("current4 has no win64 stable link: %s", body)
	}
	status, downloaded := getBody(t, prefix+link)
	if status != http.StatusOK || !bytes.Equal(downloaded, packageData) {
		t.Fatalf("GET %s = %d, %d bytes; want the published package (%d bytes)", prefix+link, status, len(downloaded), len(packageData))
	}
}

// 重新加载失败时 Handler 继续用旧快照提供服务；/readyz 仍然就绪（能服务），但标明
// 当前快照已过期，供监控发现坏清单。
func TestHandlerReadyzReportsStaleSnapshotAfterBrokenReload(t *testing.T) {
	handler, packageData, manifestPath := newWin64Handler(t, WithReloadErrorHandler(func(error) {}))
	server := httptest.NewServer(handler)
	defer server.Close()

	if status, body := getBody(t, server.URL+"/readyz"); status != http.StatusOK || !strings.Contains(string(body), `"ready"`) {
		t.Fatalf("readyz before = %d %s", status, body)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"schema_version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, body := getBody(t, server.URL+"/readyz"); status != http.StatusOK || !strings.Contains(string(body), `"stale"`) {
		t.Fatalf("readyz with a broken manifest = %d %s; want 200 stale", status, body)
	}
	if status, body := getBody(t, server.URL+"/files/td-update-win-x64-7007000"); status != http.StatusOK || !bytes.Equal(body, packageData) {
		t.Fatalf("package with a broken manifest = %d, %d bytes; want the previous snapshot's package", status, len(body))
	}
}
