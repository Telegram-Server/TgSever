package main

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// handleMessageDeletionsAPI pages the deletion log. Every query parameter is an
// optional filter; before_id continues from the previous page's last row.
func (s *server) handleMessageDeletionsAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	q := r.URL.Query()
	query := MessageDeletionQuery{
		PeerType: q.Get("peer_type"),
		Source:   q.Get("source"),
	}
	query.ActorUserID, _ = parseInt64(q.Get("actor_user_id"))
	query.OwnerUserID, _ = parseInt64(q.Get("owner_user_id"))
	query.SenderUserID, _ = parseInt64(q.Get("sender_user_id"))
	query.PeerID, _ = parseInt64(q.Get("peer_id"))
	query.MessageID, _ = parseInt(q.Get("message_id"))
	query.MinDate, _ = parseInt(q.Get("min_date"))
	query.MaxDate, _ = parseInt(q.Get("max_date"))
	query.BeforeID, _ = parseInt64(q.Get("before_id"))
	query.Limit, _ = parseInt(q.Get("limit"))
	rows, err := s.read.ListMessageDeletions(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *server) handleMessageDeletionDetailAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	id, err := parseInt64(r.URL.Query().Get("id"))
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid id")
		return
	}
	detail, err := s.read.MessageDeletionDetail(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "deletion not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
