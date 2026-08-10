package api

import (
	"net/http"
)

// handleStatus 返回状态页数据（保持稳定的公开状态 API 结构）。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opt.Scheduler.Snapshot())
}
