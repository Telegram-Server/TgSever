package adminapi

import "net/http"

// Desktop client updates. The status read is never an error for a disabled or
// external update service: the mode field says which, and the panel explains it.

func (s *Server) handleClientUpdates(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(clientUpdatesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "client updates are not configured")
		return
	}
	status, err := svc.ClientUpdateStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
