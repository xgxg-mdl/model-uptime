package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// handleSetupStatus 报告管理令牌是否已配置。公开端点，供前端决定显示
// "设置密码"还是"登录"视图。
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"token_configured": s.getAdminToken() != ""})
}

// handleSetup 首次设置管理密码。仅当尚未配置任何令牌时可用；一旦设置，
// 该端点永久失效（返回 409）。
// 新令牌原子写入配置文件，首次部署即持久化，重启不丢失。
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.getAdminToken() != "" {
		writeErr(w, http.StatusConflict, "管理密码已设置，请直接登录")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效 JSON")
		return
	}
	token := strings.TrimSpace(in.Token)
	if len(token) < 8 {
		writeErr(w, http.StatusBadRequest, "管理密码至少需要 8 个字符")
		return
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.getAdminToken() != "" {
		writeErr(w, http.StatusConflict, "管理密码已设置，请直接登录")
		return
	}
	cfg := s.currentConfig()
	cfg.AdminToken = token
	if err := s.updateConfig(&cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	s.setAdminToken(token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleLogin 校验管理令牌。成功返回 ok，前端把令牌存入会话后
// 后续请求通过 Authorization: Bearer 头携带。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是有效 JSON")
		return
	}
	if !s.tokenValid(in.Token) {
		writeErr(w, http.StatusUnauthorized, "密码无效")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// requireAuth 保护管理 API：令牌无效或未配置令牌时拒绝。
func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenValid(extractToken(r)) {
			writeErr(w, http.StatusUnauthorized, "未授权")
			return
		}
		next(w, r)
	})
}

func (s *Server) tokenValid(token string) bool {
	expected := s.getAdminToken()
	if expected == "" || token == "" {
		return false // 未配置令牌 = 所有管理操作禁用（安全默认，等待首次设置）
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
		return h[7:]
	}
	if c, err := r.Cookie("admin_token"); err == nil {
		return c.Value
	}
	return ""
}
