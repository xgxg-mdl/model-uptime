package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/lefachao/model-uptime/internal/model"
)

// decodeJSON 解析请求体 JSON。
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify 由服务名生成稳定的 id（小写、非字母数字转连字符）。
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
}

// handleListServices 返回服务列表，API key 脱敏。
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	out := make([]model.Service, len(cfg.Services))
	for i, svc := range cfg.Services {
		out[i] = maskService(svc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

// handleCreateService 新增服务并热重载。
func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	var svc model.Service
	if err := decodeJSON(r, &svc); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效 JSON")
		return
	}
	if svc.ID == "" {
		svc.ID = slugify(svc.Name)
	}
	svc.Normalize()
	if err := svc.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg := s.currentConfig()
	for _, ex := range cfg.Services {
		if ex.ID == svc.ID {
			writeErr(w, http.StatusConflict, "服务 id 已存在: "+svc.ID)
			return
		}
	}
	cfg.Services = append(cfg.Services, svc)
	if err := s.updateConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"service": maskService(svc)})
}

// handleUpdateService 更新服务。id 从路径取，请求体内的 id 仅用于改名校验。
// API key 留空或填哨兵值时保留原密钥（配置页脱敏显示的前提）。
func (s *Server) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in model.Service
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效 JSON")
		return
	}

	cfg := s.currentConfig()
	idx := -1
	for i, ex := range cfg.Services {
		if ex.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "服务不存在: "+id)
		return
	}
	old := cfg.Services[idx]

	if in.APIKey == "" || in.APIKey == model.APIKeySentinel {
		in.APIKey = old.APIKey
	}
	if in.ID == "" {
		in.ID = old.ID
	}
	if in.ID != id {
		// 不允许改名成已存在的其他 id
		for i, ex := range cfg.Services {
			if i != idx && ex.ID == in.ID {
				writeErr(w, http.StatusConflict, "服务 id 已存在: "+in.ID)
				return
			}
		}
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg.Services[idx] = in
	if err := s.updateConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service": maskService(in)})
}

// handleDeleteService 删除服务。
func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.currentConfig()
	kept := cfg.Services[:0]
	found := false
	for _, svc := range cfg.Services {
		if svc.ID == id {
			found = true
			continue
		}
		kept = append(kept, svc)
	}
	if !found {
		writeErr(w, http.StatusNotFound, "服务不存在: "+id)
		return
	}
	cfg.Services = kept
	if err := s.updateConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestService 立即探测一次，供配置页"测试连接"。
func (s *Server) handleTestService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := s.opt.Scheduler.ProbeNow(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleGetPage 返回页面显示配置。
func (s *Server) handleGetPage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.currentConfig().Page)
}

// handleUpdatePage 更新页面显示配置并热重载。
func (s *Server) handleUpdatePage(w http.ResponseWriter, r *http.Request) {
	var page model.PageConfig
	if err := decodeJSON(r, &page); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效 JSON")
		return
	}
	page.Normalize()
	if err := page.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.currentConfig()
	cfg.Page = page
	if err := s.updateConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// maskService 返回脱敏后的服务副本。
func maskService(svc model.Service) model.Service {
	svc.APIKey = maskKey(svc.APIKey)
	return svc
}

// maskKey 保留前缀便于管理员辨认是哪把密钥，其余掩码。
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "…****"
}
