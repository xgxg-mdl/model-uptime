package httpserver

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleSetupStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"token_configured": s.admin.TokenConfigured()})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.admin.TokenConfigured() {
		writeError(w, http.StatusConflict, "an admin password is already configured; sign in instead")
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := s.admin.SetupToken(request.Token); err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	if !s.admin.Authenticate(request.Token) {
		// 一个很短且固定的失败延迟限制在线猜测吞吐，不引入共享 IP 锁死问题。
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-r.Context().Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.admin.Authenticate(bearerToken(r)) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	})
}

func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
