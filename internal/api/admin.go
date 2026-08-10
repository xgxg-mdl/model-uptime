package api

import (
	"encoding/json"
	"fmt"
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

// handleDuplicateService 复制一个已存在的服务，生成不冲突的新 id 与 name。
// 用于"复制"按钮：前端列表里的 api_key 已脱敏，无法拿到明文密钥，
// 因此复制必须由服务端读取明文配置后深拷贝完成。
func (s *Server) handleDuplicateService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

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
	src := cfg.Services[idx]

	// 深拷贝：headers 是 map 引用，直接赋值会与原服务共享底层 map，
	// 后续编辑其中一个会污染另一个。
	dup := src
	dup.Headers = cloneStringMap(src.Headers)
	dup.Enabled = cloneBoolPtr(src.Enabled)
	dup.Stream = cloneBoolPtr(src.Stream)

	// 生成唯一 id：在原 id 后加 -copy / -copy2 / -copy3 …，避免 slug 碰撞。
	base := src.ID + "-copy"
	dup.ID = base
	for n := 2; ; n++ {
		taken := false
		for _, ex := range cfg.Services {
			if ex.ID == dup.ID {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		dup.ID = fmt.Sprintf("%s%d", base, n)
	}
	dup.Name = src.Name + " (copy)"

	cfg.Services = append(cfg.Services, dup)
	if err := s.updateConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"service": maskService(dup)})
}

// cloneStringMap 返回 map 的深拷贝，避免共享底层 map。
func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cloneBoolPtr 返回 *bool 的拷贝，避免复制后两个服务共享同一指针。
func cloneBoolPtr(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
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
