package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"telesrv/internal/admin"
	"telesrv/internal/updatecdn"
)

func TestClientUpdatesPanelProxyAndPermission(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Header.Get("Authorization") != "Bearer updates-secret" {
			t.Fatalf("upstream authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/client-updates" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"mode":"in_process","public_url":"https://chat.example/_updates","entries":[{"platform":"linux","channel":"stable","build":6000000}]}`))
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionUpdatesRead)
	srv.cfg.AdminAPIURL = upstream.URL
	srv.cfg.AdminAPIToken = "updates-secret"
	cookies, _ := signIn(t, srv)

	read := withCookies(httptest.NewRequest(http.MethodGet, "/api/client-updates", nil), cookies)
	readResponse := httptest.NewRecorder()
	srv.routes().ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK || upstreamCalls != 1 ||
		!strings.Contains(readResponse.Body.String(), `"mode":"in_process"`) ||
		readResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read status=%d calls=%d cache=%q body=%s", readResponse.Code, upstreamCalls,
			readResponse.Header().Get("Cache-Control"), readResponse.Body.String())
	}

	// 只有发布权限的会话也看不到状态：查看权限要单独授予。
	forbidden := panelServer(t, permissionUpdatesPublish)
	forbidden.cfg.AdminAPIURL = upstream.URL
	forbiddenCookies, _ := signIn(t, forbidden)
	forbiddenResponse := httptest.NewRecorder()
	forbidden.routes().ServeHTTP(forbiddenResponse,
		withCookies(httptest.NewRequest(http.MethodGet, "/api/client-updates", nil), forbiddenCookies))
	if forbiddenResponse.Code != http.StatusForbidden || upstreamCalls != 1 ||
		!strings.Contains(forbiddenResponse.Body.String(), permissionUpdatesRead) {
		t.Fatalf("forbidden status=%d calls=%d body=%s", forbiddenResponse.Code, upstreamCalls, forbiddenResponse.Body.String())
	}
}

func TestClientUpdateUploadStreamsToAdminAPI(t *testing.T) {
	payload := bytes.Repeat([]byte("td-update!"), 100*1024)
	var gotBody []byte
	var gotQuery url.Values
	var gotLength int64
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/client-updates/uploads" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer updates-secret" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("upstream headers = %v", r.Header)
		}
		gotQuery, gotLength = r.URL.Query(), r.ContentLength
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(admin.CommandResult{
			CommandID: gotQuery.Get("command_id"), Action: admin.ActionPreflightClientUpdate, Status: "completed", DryRun: true,
			Details: map[string]any{"staging_id": "0123456789abcdef0123456789abcdef"},
		})
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionUpdatesPublish)
	srv.cfg.AdminAPIURL = upstream.URL
	srv.cfg.AdminAPIToken = "updates-secret"
	cookies, csrf := signIn(t, srv)

	query := url.Values{"reason": {"ship 6.0.1"}, "allow_downgrade": {"true"}}.Encode()
	upload := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/client-update-upload?"+query, bytes.NewReader(payload)), cookies)
	upload.Header.Set(csrfHeaderName, csrf)
	upload.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, upload)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"staging_id":"0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(gotBody, payload) || gotLength != int64(len(payload)) {
		t.Fatalf("upstream got %d bytes (content-length %d), want the %d uploaded bytes unchanged", len(gotBody), gotLength, len(payload))
	}
	if gotQuery.Get("actor") != "admin" || gotQuery.Get("reason") != "ship 6.0.1" || gotQuery.Get("allow_downgrade") != "true" ||
		!strings.HasPrefix(gotQuery.Get("command_id"), "dry-client-update-") {
		t.Fatalf("upstream metadata = %v", gotQuery)
	}

	// 缺理由、不是原始字节、声明超过上限的上传都在面板侧拒绝，不转发。
	for name, mutate := range map[string]func(*http.Request){
		"no reason":  func(r *http.Request) { r.URL.RawQuery = "" },
		"multipart":  func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=x") },
		"over limit": func(r *http.Request) { r.ContentLength = updatecdn.MaxDesktopPackageSize + 1 },
	} {
		req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/client-update-upload?"+query, strings.NewReader("x")), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		req.Header.Set("Content-Type", "application/octet-stream")
		mutate(req)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("%s: status=%d body=%s, want a 4xx", name, rec.Code, rec.Body.String())
		}
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream called %d times, want only the valid upload forwarded", upstreamCalls)
	}

	// 只有查看权限的会话不能上传。
	viewer := panelServer(t, permissionUpdatesRead)
	viewer.cfg.AdminAPIURL = upstream.URL
	viewerCookies, viewerCSRF := signIn(t, viewer)
	denied := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/client-update-upload?"+query, bytes.NewReader(payload)), viewerCookies)
	denied.Header.Set(csrfHeaderName, viewerCSRF)
	denied.Header.Set("Content-Type", "application/octet-stream")
	rec = httptest.NewRecorder()
	viewer.routes().ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden || upstreamCalls != 1 || !strings.Contains(rec.Body.String(), permissionUpdatesPublish) {
		t.Fatalf("viewer upload status=%d calls=%d body=%s", rec.Code, upstreamCalls, rec.Body.String())
	}
}

func TestClientUpdateUploadPassesUpstreamFailureThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: "dry-x", Status: "failed", Error: "UPDATE_PACKAGE_REJECTED: signature does not verify", Code: admin.CodeUpdatePackageRejected})
	}))
	defer upstream.Close()
	srv := panelServer(t, permissionUpdatesPublish)
	srv.cfg.AdminAPIURL = upstream.URL
	cookies, csrf := signIn(t, srv)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/client-update-upload?reason=r", strings.NewReader("forged")), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"UPDATE_PACKAGE_REJECTED"`) {
		t.Fatalf("status=%d body=%s, want the upstream 400 and its coded result", rec.Code, rec.Body.String())
	}
}

func TestClientUpdatePublishProxiesToAdminAPI(t *testing.T) {
	var got admin.PublishClientUpdateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/client-updates/publish" || r.Header.Get("Authorization") != "Bearer updates-secret" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: got.CommandID, Action: admin.ActionPublishClientUpdate, Status: "completed", DryRun: got.DryRun})
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionUpdatesPublish)
	srv.cfg.AdminAPIURL = upstream.URL
	srv.cfg.AdminAPIToken = "updates-secret"
	cookies, csrf := signIn(t, srv)
	body := `{"reason":"roll back 6.0.1","confirm":true,"command_id":"dry-client-update-publish-1","staging_id":"0123456789abcdef0123456789abcdef","allow_downgrade":true}`
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/client-update-publish", strings.NewReader(body)), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got.DryRun || !strings.HasPrefix(got.CommandID, "exec-") || got.Actor != "admin" ||
		got.StagingID != "0123456789abcdef0123456789abcdef" || !got.AllowDowngrade || got.Reason != "roll back 6.0.1" {
		t.Fatalf("status=%d request=%+v body=%s", rec.Code, got, rec.Body.String())
	}

	viewer := panelServer(t, permissionUpdatesRead)
	viewer.cfg.AdminAPIURL = upstream.URL
	viewerCookies, viewerCSRF := signIn(t, viewer)
	denied := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/client-update-publish", strings.NewReader(body)), viewerCookies)
	denied.Header.Set(csrfHeaderName, viewerCSRF)
	rec = httptest.NewRecorder()
	viewer.routes().ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer publish status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClientUpdateDisableEnableProxyToAdminAPI(t *testing.T) {
	got := map[string]admin.ClientUpdateEntryRequest{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer updates-secret" {
			http.NotFound(w, r)
			return
		}
		var req admin.ClientUpdateEntryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		got[r.URL.Path] = req
		_ = json.NewEncoder(w).Encode(admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun})
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionUpdatesPublish)
	srv.cfg.AdminAPIURL = upstream.URL
	srv.cfg.AdminAPIToken = "updates-secret"
	cookies, csrf := signIn(t, srv)
	viewer := panelServer(t, permissionUpdatesRead)
	viewer.cfg.AdminAPIURL = upstream.URL
	viewerCookies, viewerCSRF := signIn(t, viewer)

	for panelPath, upstreamPath := range map[string]string{
		"/api/actions/client-update-disable": "/v1/client-updates/disable",
		"/api/actions/client-update-enable":  "/v1/client-updates/enable",
	} {
		body := `{"reason":"crashes on start","confirm":true,"command_id":"dry-x-1","platform":"linux","channel":"stable"}`
		req := withCookies(httptest.NewRequest(http.MethodPost, panelPath, strings.NewReader(body)), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		sent, ok := got[upstreamPath]
		if rec.Code != http.StatusOK || !ok || sent.DryRun || !strings.HasPrefix(sent.CommandID, "exec-") || sent.Actor != "admin" ||
			sent.Platform != "linux" || sent.Channel != "stable" || sent.Reason != "crashes on start" {
			t.Fatalf("%s status=%d request=%+v body=%s", panelPath, rec.Code, sent, rec.Body.String())
		}

		// 未确认是 dry-run。
		preview := withCookies(httptest.NewRequest(http.MethodPost, panelPath, strings.NewReader(`{"reason":"r","platform":"linux","channel":"stable"}`)), cookies)
		preview.Header.Set(csrfHeaderName, csrf)
		rec = httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, preview)
		if sent := got[upstreamPath]; rec.Code != http.StatusOK || !sent.DryRun {
			t.Fatalf("%s preview status=%d request=%+v", panelPath, rec.Code, sent)
		}

		delete(got, upstreamPath)
		denied := withCookies(httptest.NewRequest(http.MethodPost, panelPath, strings.NewReader(body)), viewerCookies)
		denied.Header.Set(csrfHeaderName, viewerCSRF)
		rec = httptest.NewRecorder()
		viewer.routes().ServeHTTP(rec, denied)
		if _, reached := got[upstreamPath]; rec.Code != http.StatusForbidden || reached || !strings.Contains(rec.Body.String(), permissionUpdatesPublish) {
			t.Fatalf("%s viewer status=%d body=%s", panelPath, rec.Code, rec.Body.String())
		}
	}
}

func TestClientUpdatePermissionsAreAssignable(t *testing.T) {
	assignable := make(map[string]bool)
	for _, name := range assignablePermissions() {
		assignable[name] = true
	}
	for _, want := range []string{"updates.read", "updates.publish"} {
		if !assignable[want] {
			t.Errorf("%q missing from assignablePermissions: no named operator could ever be granted it", want)
		}
	}
	if err := validatePermissions([]string{permissionUpdatesRead, permissionUpdatesPublish}); err != nil {
		t.Fatalf("validatePermissions(updates rights) = %v", err)
	}
}
