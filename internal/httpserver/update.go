package httpserver

import "net/http"

func (s *Server) handleGetUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeUpdateStatus(w, r, false)
}

func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeUpdateStatus(w, r, true)
}

func (s *Server) writeUpdateStatus(w http.ResponseWriter, r *http.Request, force bool) {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "update service is not configured")
		return
	}
	status, err := s.updater.Check(r.Context(), force)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleStartUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeError(w, http.StatusServiceUnavailable, "update service is not configured")
		return
	}
	status, err := s.updater.Check(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !status.Enabled {
		writeError(w, http.StatusConflict, status.DisabledReason)
		return
	}
	if !status.UpdateAvailable {
		writeError(w, http.StatusConflict, "already running the latest version")
		return
	}
	if err := s.updater.Start(status.LatestVersion); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":             true,
		"target_version": status.LatestVersion,
	})
}
