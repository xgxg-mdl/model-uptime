// Package api 提供 HTTP 层：公开状态 API、受 token 保护的管理 API、静态页面。
package api

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"

	"github.com/lefachao/model-uptime/internal/config"
	"github.com/lefachao/model-uptime/internal/model"
	"github.com/lefachao/model-uptime/internal/notifier"
	"github.com/lefachao/model-uptime/internal/scheduler"
)

//go:embed web
var webFS embed.FS

// Options 是 Server 的依赖注入。
type Options struct {
	Scheduler  *scheduler.Scheduler
	Notifier   *notifier.Notifier
	ConfigPath string // config.yaml 路径（admin 修改后原子写回）
	AdminToken string // 已生效的管理令牌（env 优先于配置文件）
	Logger     *slog.Logger
}

// Server 持有 HTTP 处理器所需的全部依赖与运行时配置。
type Server struct {
	opt   Options
	log   *slog.Logger
	cfgMu sync.RWMutex
	cfg   *config.Config // 运行时配置快照，admin 修改后同步落盘
	// updateMu 覆盖“读取当前配置 → 修改 → 落盘 → 热更新”的完整事务，
	// 避免并发管理请求互相覆盖并造成磁盘与运行时状态分叉。
	updateMu sync.Mutex
	// 生效中的管理令牌。初值来自 opt.AdminToken；首次设置（/api/admin/setup）
	// 会写入配置文件并更新此字段，无需重启即生效。
	tokenMu    sync.RWMutex
	adminToken string
}

// New 创建 Server 并初始化路由。
func New(o Options, cfg *config.Config) (*Server, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := &Server{opt: o, log: o.Logger, cfg: cfg, adminToken: o.AdminToken}
	return s, nil
}

func (s *Server) getAdminToken() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	return s.adminToken
}

func (s *Server) setAdminToken(t string) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	s.adminToken = t
}

// Handler 返回完整 HTTP 路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开 API
	mux.HandleFunc("GET /api/status", s.handleStatus)

	// 管理 API（token 保护）
	mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	// 首次设置管理密码（仅当令牌未配置时可用）
	mux.HandleFunc("GET /api/admin/setup-status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/admin/setup", s.handleSetup)
	mux.Handle("GET /api/admin/services", s.requireAuth(s.handleListServices))
	mux.Handle("POST /api/admin/services", s.requireAuth(s.handleCreateService))
	mux.Handle("PATCH /api/admin/services", s.requireAuth(s.handleBulkUpdateServices))
	mux.Handle("PUT /api/admin/services/{id}", s.requireAuth(s.handleUpdateService))
	mux.Handle("DELETE /api/admin/services/{id}", s.requireAuth(s.handleDeleteService))
	mux.Handle("POST /api/admin/services/{id}/test", s.requireAuth(s.handleTestService))
	mux.Handle("POST /api/admin/services/{id}/duplicate", s.requireAuth(s.handleDuplicateService))
	mux.Handle("GET /api/admin/page", s.requireAuth(s.handleGetPage))
	mux.Handle("PUT /api/admin/page", s.requireAuth(s.handleUpdatePage))
	mux.Handle("GET /api/admin/telegram", s.requireAuth(s.handleGetTelegram))
	mux.Handle("PUT /api/admin/telegram", s.requireAuth(s.handleUpdateTelegram))
	mux.Handle("POST /api/admin/telegram/test", s.requireAuth(s.handleTestTelegram))

	// 静态页面（状态页 + 配置页 + 字体）。webFS 根含 web/ 前缀，需 Sub 到内容根，
	// 使 / 正确落到 index.html，而非显示 embed 目录结构。
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// embed 路径在编译期校验，运行时不可能失败
		panic(err)
	}
	mux.Handle("GET /admin", s.page("admin/index.html"))
	mux.Handle("/", http.FileServer(http.FS(sub)))

	return logRequests(s.log, mux)
}

// page 返回指定嵌入文件的处理器。
func (s *Server) page(name string) http.HandlerFunc {
	data, err := fs.ReadFile(webFS, "web/"+name)
	if err != nil {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "页面缺失", http.StatusInternalServerError)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

// currentConfig 返回当前配置的只读副本。
func (s *Server) currentConfig() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	out := *s.cfg
	out.Services = append([]model.Service(nil), s.cfg.Services...)
	for i := range out.Services {
		out.Services[i].Headers = cloneStringMap(out.Services[i].Headers)
		out.Services[i].Enabled = cloneBoolPtr(out.Services[i].Enabled)
		out.Services[i].Stream = cloneBoolPtr(out.Services[i].Stream)
	}
	out.Telegram.Subscriptions = append([]notifier.Subscription(nil), s.cfg.Telegram.Subscriptions...)
	for i := range out.Telegram.Subscriptions {
		out.Telegram.Subscriptions[i].ServiceIDs = append([]string(nil), out.Telegram.Subscriptions[i].ServiceIDs...)
	}
	return out
}

// updateConfig 校验、落盘并应用新配置，随后通知调度器热重载。
// 返回校验/写盘错误；失败时配置不变。
func (s *Server) updateConfig(next *config.Config) error {
	next.Normalize()
	if err := next.Validate(); err != nil {
		return err
	}
	if err := next.Save(s.opt.ConfigPath); err != nil {
		return err
	}
	if s.opt.Notifier != nil {
		if err := s.opt.Notifier.UpdateConfig(next.Telegram); err != nil {
			return err
		}
	}
	s.cfgMu.Lock()
	s.cfg = next
	s.cfgMu.Unlock()
	s.opt.Scheduler.Reload(next.Services, next.Page)
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// logRequests 记录每个请求的方法、路径与状态码。
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", rec.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
