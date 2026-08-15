package api

import (
	"net/http"
)

// handleGetUpdate 返回缓存的版本检查结果；缓存过期时会刷新远端状态。
func (s *Server) handleGetUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeUpdateStatus(w, r, false)
}

// handleCheckUpdate 强制刷新 GitHub Tag 与 GHCR 镜像发布状态。
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeUpdateStatus(w, r, true)
}

func (s *Server) writeUpdateStatus(w http.ResponseWriter, r *http.Request, force bool) {
	if s.opt.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "update service is not configured")
		return
	}
	status, err := s.opt.Updater.Check(r.Context(), force)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleStartUpdate 在确认目标镜像已发布后异步触发更新。异步触发确保
// 当前容器被替换前，浏览器能够先收到 202 并进入重启轮询状态。
func (s *Server) handleStartUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opt.Updater == nil {
		writeErr(w, http.StatusServiceUnavailable, "update service is not configured")
		return
	}
	status, err := s.opt.Updater.Check(r.Context(), true)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !status.Enabled {
		writeErr(w, http.StatusConflict, status.DisabledReason)
		return
	}
	if !status.UpdateAvailable {
		writeErr(w, http.StatusConflict, "already running the latest version")
		return
	}
	if err := s.opt.Updater.Start(status.LatestVersion); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":             true,
		"target_version": status.LatestVersion,
	})
}
