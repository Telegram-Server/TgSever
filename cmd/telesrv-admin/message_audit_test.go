package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The audit viewer reads live and deleted history and message media. All of
// it is gated by messages.read; deletion attribution stays behind its own
// right and is fetched from the deletion-log endpoint, never from these.
func TestMessageAuditRoutesRequireMessagesRead(t *testing.T) {
	paths := []string{
		"/api/messages/audit/dialogs?owner_user_id=1",
		"/api/messages/audit/history?owner_user_id=1&peer_type=user&peer_id=2",
		"/api/messages/audit/search?owner_user_id=1&peer_type=user&peer_id=2&q=hi",
		"/api/messages/media/photo/7",
		"/api/messages/media/document/21",
	}
	for _, path := range paths {
		rec := httptest.NewRecorder()
		panelServer(t, permissionAll).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without a session status=%d, want 401", path, rec.Code)
		}
	}
	srv := panelServer(t, permissionMessageDeletionsRead)
	cookies, _ := signIn(t, srv)
	for _, path := range paths {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s without messages.read status=%d, want 403", path, rec.Code)
		}
	}
}

// The panel relays message files from the admin API. The upstream's
// attachment and nosniff headers must survive the hop: dropping them would let
// an uploaded HTML file render inside the panel's origin.
func TestMessageMediaProxyKeepsTheDownloadHeaders(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		if r.URL.Path == "/v1/media/documents/404" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Disposition", `attachment; filename="document-21"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte("<html>"))
	}))
	defer api.Close()
	srv := &server{cfg: uiConfig{AdminAPIURL: api.URL, AdminAPIToken: "api-secret"}}

	req := httptest.NewRequest(http.MethodGet, "/api/messages/media/document/21?thumb=m&evil=1", nil)
	req.SetPathValue("id", "21")
	rec := httptest.NewRecorder()
	srv.handleMessageDocumentAPI(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/media/documents/21" || gotQuery != "thumb=m" || gotAuth != "Bearer api-secret" {
		t.Fatalf("upstream path=%q query=%q auth=%q, want only the thumb parameter forwarded", gotPath, gotQuery, gotAuth)
	}
	if rec.Header().Get("Content-Disposition") != `attachment; filename="document-21"` || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers=%v, want the attachment and nosniff relayed", rec.Header())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/messages/media/document/404", nil)
	req.SetPathValue("id", "404")
	rec = httptest.NewRecorder()
	srv.handleMessageDocumentAPI(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing document status=%d, want 404", rec.Code)
	}
}
