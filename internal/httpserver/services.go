package httpserver

import (
	"net/http"

	"github.com/xgxg-mdl/model-uptime/internal/admin"
	"github.com/xgxg-mdl/model-uptime/internal/model"
)

func (s *Server) handleListServices(w http.ResponseWriter, _ *http.Request) {
	services := s.admin.Snapshot().Services
	masked := make([]model.Service, len(services))
	for index := range services {
		masked[index] = maskService(services[index])
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": masked})
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var service model.Service
	if err := decodeJSON(w, r, &service); err != nil {
		writeDecodeError(w, err)
		return
	}
	created, err := s.admin.CreateService(service)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"service": maskService(created)})
}

func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	var service model.Service
	if err := decodeJSON(w, r, &service); err != nil {
		writeDecodeError(w, err)
		return
	}
	updated, err := s.admin.UpdateService(r.PathValue("id"), service)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service": maskService(updated)})
}

func (s *Server) handleDuplicateService(w http.ResponseWriter, r *http.Request) {
	duplicated, err := s.admin.DuplicateService(r.PathValue("id"))
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"service": maskService(duplicated)})
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	if err := s.admin.DeleteService(r.PathValue("id")); err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTestService(w http.ResponseWriter, r *http.Request) {
	result, err := s.admin.ProbeNow(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type servicePatchRequest struct {
	Enabled     *bool `json:"enabled,omitempty"`
	IntervalSec *int  `json:"interval_sec,omitempty"`
	TimeoutSec  *int  `json:"timeout_sec,omitempty"`
	Stream      *bool `json:"stream,omitempty"`
}

func (s *Server) handleBulkUpdateServices(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IDs   []string            `json:"ids"`
		Patch servicePatchRequest `json:"patch"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	services, err := s.admin.UpdateServices(request.IDs, admin.ServicePatch{
		Enabled: request.Patch.Enabled, IntervalSec: request.Patch.IntervalSec,
		TimeoutSec: request.Patch.TimeoutSec, Stream: request.Patch.Stream,
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	for index := range services {
		services[index] = maskService(services[index])
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}

func maskService(service model.Service) model.Service {
	service.APIKey = maskKey(service.APIKey)
	if service.Enabled == nil {
		enabled := true
		service.Enabled = &enabled
	}
	return service
}

func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "…****"
}
