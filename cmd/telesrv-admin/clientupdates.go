package main

import "net/http"

// Desktop client updates. The panel only forwards: the update catalog lives in
// the telesrv process (admin API, 2599), so the admin container needs neither
// the update volume nor the update root public key.

func (s *server) handleClientUpdatesAPI(w http.ResponseWriter, r *http.Request) {
	s.proxyAdminJSONNoStore(w, r, "/v1/client-updates", 1<<20)
}
