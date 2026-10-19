package updatecdn

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
)

const maxResolveQueryLength = 256

// PublicWebPrefix 是主进程把 Handler 挂到公开 Web 上的路径前缀。公开 Web 按它剥离
// 前缀后转发，配置按它推导 autoupdate_url_prefix（<PublicBaseURL>/_updates）。用户名
// 不能以下划线开头，所以它不会遮蔽 /{username} 落地页。
const PublicWebPrefix = "/_updates"

type Handler struct {
	store *Store
	mux   *http.ServeMux
}

func NewHandler(store *Store) (*Handler, error) {
	if store == nil {
		return nil, fmt.Errorf("update catalog store is required")
	}
	h := &Handler{store: store, mux: http.NewServeMux()}
	h.mux.HandleFunc("/healthz", h.health)
	h.mux.HandleFunc("/readyz", h.ready)
	h.mux.HandleFunc("/v1/resolve", h.resolve)
	h.mux.HandleFunc("/files/", h.file)
	for _, endpoint := range []string{"/current", "/current1", "/current2", "/current3", "/current4"} {
		h.mux.HandleFunc(endpoint, h.current)
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if !allowReadMethod(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	}
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if !allowReadMethod(w, r) {
		return
	}
	_, stale, err := h.store.snapshot()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "catalog unavailable")
		return
	}
	// 新清单加载失败时仍在用上一份正常快照服务，因此保持就绪（200），只把状态标为
	// stale 供监控发现；具体原因不对外暴露，由 Store 的重新加载回调记录到日志。
	body := "{\"status\":\"ready\"}\n"
	if stale != nil {
		body = "{\"status\":\"stale\",\"error\":\"manifest reload failed; serving the previous catalog\"}\n"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

func (h *Handler) current(w http.ResponseWriter, r *http.Request) {
	if !allowReadMethod(w, r) {
		return
	}
	catalog, err := h.store.Snapshot()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "catalog unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := json.NewEncoder(w).Encode(catalog.DesktopMap()); err != nil {
		return
	}
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) {
	if !allowReadMethod(w, r) {
		return
	}
	query := r.URL.Query()
	values := []string{query.Get("platform"), query.Get("channel"), query.Get("version"), query.Get("source"), query.Get("lang_code")}
	for _, value := range values {
		if len(value) > maxResolveQueryLength {
			writeJSONError(w, http.StatusBadRequest, "query value too long")
			return
		}
	}
	if strings.TrimSpace(values[0]) == "" {
		writeJSONError(w, http.StatusBadRequest, "platform is required")
		return
	}
	catalog, err := h.store.Snapshot()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "catalog unavailable")
		return
	}
	resolved, err := catalog.Resolve(ResolveRequest{
		Platform: values[0],
		Channel:  values[1],
		Version:  values[2],
		Source:   values[3],
		LangCode: values[4],
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "resolve failed")
		return
	}
	if resolved == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(resolved)
}

func (h *Handler) file(w http.ResponseWriter, r *http.Request) {
	if !allowReadMethod(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/files/")
	if name == "" || path.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		http.NotFound(w, r)
		return
	}
	catalog, err := h.store.Snapshot()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "catalog unavailable")
		return
	}
	record, ok := catalog.file(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	etag := `"sha256-` + record.sha256 + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f, err := os.Open(record.path)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "package unavailable")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != record.size || !info.ModTime().Equal(record.modTime) {
		writeJSONError(w, http.StatusServiceUnavailable, "package changed; reload the manifest")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", record.name))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, record.name, record.modTime, f)
}

func allowReadMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
