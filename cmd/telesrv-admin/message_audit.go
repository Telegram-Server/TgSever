package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// handleAuditDialogsAPI lists one account's chats for the audit viewer.
func (s *server) handleAuditDialogsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	query := AuditDialogQuery{Kind: q.Get("kind"), Q: q.Get("q")}
	query.OwnerUserID, _ = parseInt64(q.Get("owner_user_id"))
	query.Offset, _ = parseInt(q.Get("offset"))
	query.Limit, _ = parseInt(q.Get("limit"))
	if query.OwnerUserID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "owner_user_id is required")
		return
	}
	rows, err := s.read.ListAuditDialogs(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// handleAuditHistoryAPI pages one chat's transcript, deleted messages included.
func (s *server) handleAuditHistoryAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	query := AuditHistoryQuery{PeerType: q.Get("peer_type")}
	query.OwnerUserID, _ = parseInt64(q.Get("owner_user_id"))
	query.PeerID, _ = parseInt64(q.Get("peer_id"))
	query.BeforeID, _ = parseInt(q.Get("before_id"))
	query.AfterID, _ = parseInt(q.Get("after_id"))
	query.AroundID, _ = parseInt(q.Get("around_id"))
	query.Limit, _ = parseInt(q.Get("limit"))
	if query.OwnerUserID <= 0 || query.PeerID <= 0 || (query.PeerType != "user" && query.PeerType != "channel") {
		writeAPIError(w, http.StatusBadRequest, "owner_user_id, peer_type (user/channel) and peer_id are required")
		return
	}
	page, err := s.read.AuditHistory(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// handleAuditSearchAPI searches one chat's text, deleted messages included.
func (s *server) handleAuditSearchAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	query := AuditSearchQuery{PeerType: q.Get("peer_type"), Q: q.Get("q")}
	query.OwnerUserID, _ = parseInt64(q.Get("owner_user_id"))
	query.PeerID, _ = parseInt64(q.Get("peer_id"))
	query.SenderUserID, _ = parseInt64(q.Get("sender_user_id"))
	query.Limit, _ = parseInt(q.Get("limit"))
	rows, err := s.read.AuditSearch(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// maxProxiedMessageFile bounds a relayed message file; the admin API refuses
// anything larger, so this only guards against a misbehaving upstream.
const maxProxiedMessageFile = 21 << 20

// handleMessagePhotoAPI relays a message photo (?full=1 for the largest size).
func (s *server) handleMessagePhotoAPI(w http.ResponseWriter, r *http.Request) {
	query := url.Values{}
	if r.URL.Query().Get("full") == "1" {
		query.Set("full", "1")
	}
	s.proxyMessageMedia(w, r, "photos", query)
}

// handleMessageDocumentAPI relays a document thumbnail (?thumb=<type>) or the
// file itself.
func (s *server) handleMessageDocumentAPI(w http.ResponseWriter, r *http.Request) {
	query := url.Values{}
	if thumb := r.URL.Query().Get("thumb"); thumb != "" {
		query.Set("thumb", thumb)
	}
	s.proxyMessageMedia(w, r, "documents", query)
}

// proxyMessageMedia forwards only the parameters it was given, and relays the
// upstream's content headers unchanged: the admin API decides what is a safe
// inline image and what must be a sniff-proof download, and that decision has
// to survive the hop.
func (s *server) proxyMessageMedia(w http.ResponseWriter, r *http.Request, kind string, query url.Values) {
	id, err := parseInt64(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	target := fmt.Sprintf("%s/v1/media/%s/%d", s.cfg.AdminAPIURL, kind, id)
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProxiedMessageFile+1))
	if err != nil || len(data) == 0 || len(data) > maxProxiedMessageFile {
		http.NotFound(w, r)
		return
	}
	for _, header := range []string{"Content-Type", "Content-Disposition", "X-Content-Type-Options"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
